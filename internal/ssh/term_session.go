package ssh

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"golang.org/x/crypto/ssh"

	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
)

const MaxScrollbackLines = 2000

// ScreenSelection is a rectangular text selection expressed in terminal
// screen coordinates (column 0 / row 0 = top-left of the live PTY screen).
// It is used by the TUI to support mouse text selection + copy inside the
// embedded shell, since Bubble Tea consumes native mouse events.
type ScreenSelection struct {
	Active bool
	X0, Y0 int
	X1, Y1 int
}

func (sel ScreenSelection) bounds(w, h int) (int, int, int, int) {
	x0, x1 := sel.X0, sel.X1
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	y0, y1 := sel.Y0, sel.Y1
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 >= w {
		x1 = w - 1
	}
	if y1 >= h {
		y1 = h - 1
	}
	return x0, y0, x1, y1
}

// TermSession wraps an authenticated SSH PTY shell session paired with a
// pure-Go VT emulator and bounded scrollback buffer.
type TermSession struct {
	mu             sync.RWMutex
	emu            *vt.Emulator
	client         *ssh.Client
	session        *ssh.Session
	stdin          io.WriteCloser
	transcript     *os.File
	transcriptPath string

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closed    bool

	stdinCh chan []byte
	dirtyCh chan struct{}
	doneCh  chan error
}

// StartTermSession dials host, allocates an xterm-256color PTY, starts the remote
// shell, and wires all background I/O loops with strict context cleanup.
func StartTermSession(
	parentCtx context.Context,
	host db.Host,
	kc *db.KeychainItem,
	vault *crypto.Vault,
	knownHostsPath string,
	logsDir string,
	cols, rows int,
	overrideSecret string,
	onStep func(string),
) (*TermSession, error) {
	if cols < 10 {
		cols = 80
	}
	if rows < 4 {
		rows = 24
	}

	client, err := DialHostWithProgress(parentCtx, host, kc, vault, knownHostsPath, overrideSecret, onStep)
	if err != nil {
		return nil, err
	}

	if onStep != nil {
		onStep("Allocating PTY")
	}

	sshSess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("open ssh session: %w", err)
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sshSess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		_ = sshSess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("request pty: %w", err)
	}

	stdinPipe, err := sshSess.StdinPipe()
	if err != nil {
		_ = sshSess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := sshSess.StdoutPipe()
	if err != nil {
		_ = sshSess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := sshSess.StderrPipe()
	if err != nil {
		_ = sshSess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	var tFile *os.File
	var tPath string
	if host.RecordSession && strings.TrimSpace(logsDir) != "" {
		if p, prepErr := prepareTranscriptFile(logsDir, host.Name); prepErr == nil {
			if f, openErr := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o600); openErr == nil {
				tFile = f
				tPath = p
			}
		}
	}

	if err := sshSess.Shell(); err != nil {
		if tFile != nil {
			_ = tFile.Close()
		}
		_ = sshSess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("start remote shell: %w", err)
	}

	emu := vt.NewEmulator(cols, rows)
	emu.SetScrollbackSize(MaxScrollbackLines)

	ctx, cancel := context.WithCancel(parentCtx)
	ts := &TermSession{
		emu:            emu,
		client:         client,
		session:        sshSess,
		stdin:          stdinPipe,
		transcript:     tFile,
		transcriptPath: tPath,
		ctx:            ctx,
		cancel:         cancel,
		stdinCh:        make(chan []byte, 256),
		dirtyCh:        make(chan struct{}, 1),
		doneCh:         make(chan error, 1),
	}

	ts.startLoops(stdoutPipe, stderrPipe)
	return ts, nil
}

func (s *TermSession) startLoops(stdoutPipe, stderrPipe io.Reader) {
	// 1. Drain VT emulator pipe (key presses + DSR/OSC replies) without blocking Write/SendKey.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		buf := make([]byte, 4096)
		for {
			if s.ctx.Err() != nil {
				return
			}
			n, err := s.emu.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case s.stdinCh <- chunk:
				case <-s.ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// 2. Write outgoing bytes to SSH stdin.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-s.ctx.Done():
				return
			case data, ok := <-s.stdinCh:
				if !ok {
					return
				}
				if _, err := s.stdin.Write(data); err != nil {
					return
				}
			}
		}
	}()

	// 3 & 4. Stream SSH stdout and stderr into VT emulator.
	readRemote := func(r io.Reader) {
		defer s.wg.Done()
		buf := make([]byte, 16384)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				s.mu.Lock()
				if s.emu != nil && !s.closed {
					_, _ = s.emu.Write(chunk)
				}
				if s.transcript != nil && !s.closed {
					_, _ = s.transcript.Write(chunk)
				}
				s.mu.Unlock()

				select {
				case s.dirtyCh <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}
	s.wg.Add(2)
	go readRemote(stdoutPipe)
	go readRemote(stderrPipe)

	// 5. Wait for remote shell exit.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := s.session.Wait()
		select {
		case s.doneCh <- err:
		default:
		}
	}()
}

// Dirty returns a channel that receives a signal when terminal contents change.
func (s *TermSession) Dirty() <-chan struct{} {
	return s.dirtyCh
}

// Done returns a channel that receives the remote shell exit error when closed.
func (s *TermSession) Done() <-chan error {
	return s.doneCh
}

// TranscriptPath returns the path to the recorded session transcript (or empty string).
func (s *TermSession) TranscriptPath() string {
	return s.transcriptPath
}

// Resize resizes both the local VT emulator and the remote SSH PTY.
func (s *TermSession) Resize(cols, rows int) {
	if cols < 4 || rows < 2 {
		return
	}
	s.mu.Lock()
	if s.emu == nil || s.closed {
		s.mu.Unlock()
		return
	}
	if s.emu.Width() == cols && s.emu.Height() == rows {
		s.mu.Unlock()
		return
	}
	s.emu.Resize(cols, rows)
	sess := s.session
	s.mu.Unlock()

	if sess != nil {
		_ = sess.WindowChange(rows, cols)
	}
}

// WriteKey forwards a Bubble Tea key event into the VT emulator / SSH PTY.
func (s *TermSession) WriteKey(msg tea.KeyMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emu == nil || s.closed {
		return
	}

	if msg.Paste && len(msg.Runes) > 0 {
		s.emu.Paste(string(msg.Runes))
		return
	}

	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		if !msg.Alt {
			s.emu.SendText(string(msg.Runes))
			return
		}
		for _, r := range msg.Runes {
			s.emu.SendKey(uv.KeyPressEvent{Code: r, Text: string(r), Mod: vt.ModAlt})
		}
		return
	}

	var mod vt.KeyMod
	if msg.Alt {
		mod |= vt.ModAlt
	}

	switch msg.Type {
	case tea.KeyEnter:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyEnter, Mod: mod})
	case tea.KeyTab:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyTab, Mod: mod})
	case tea.KeyShiftTab:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyTab, Mod: mod | vt.ModShift})
	case tea.KeyBackspace:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyBackspace, Mod: mod})
	case tea.KeyEsc:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyEscape, Mod: mod})
	case tea.KeySpace:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeySpace, Text: " ", Mod: mod})
	case tea.KeyUp:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyUp, Mod: mod})
	case tea.KeyDown:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyDown, Mod: mod})
	case tea.KeyRight:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyRight, Mod: mod})
	case tea.KeyLeft:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyLeft, Mod: mod})
	case tea.KeyHome:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyHome, Mod: mod})
	case tea.KeyEnd:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyEnd, Mod: mod})
	case tea.KeyPgUp:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyPgUp, Mod: mod})
	case tea.KeyPgDown:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyPgDown, Mod: mod})
	case tea.KeyDelete:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyDelete, Mod: mod})
	case tea.KeyInsert:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyInsert, Mod: mod})
	case tea.KeyF1:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF1, Mod: mod})
	case tea.KeyF2:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF2, Mod: mod})
	case tea.KeyF3:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF3, Mod: mod})
	case tea.KeyF4:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF4, Mod: mod})
	case tea.KeyF5:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF5, Mod: mod})
	case tea.KeyF6:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF6, Mod: mod})
	case tea.KeyF7:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF7, Mod: mod})
	case tea.KeyF8:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF8, Mod: mod})
	case tea.KeyF9:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF9, Mod: mod})
	case tea.KeyF10:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF10, Mod: mod})
	case tea.KeyF11:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF11, Mod: mod})
	case tea.KeyF12:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeyF12, Mod: mod})
	case tea.KeyCtrlAt:
		s.emu.SendKey(uv.KeyPressEvent{Code: vt.KeySpace, Mod: mod | vt.ModCtrl})
	case tea.KeyCtrlCloseBracket:
		s.sendRawCtrl(0x1d, msg.Alt)
	case tea.KeyCtrlBackslash:
		s.sendRawCtrl(0x1c, msg.Alt)
	case tea.KeyCtrlCaret:
		s.sendRawCtrl(0x1e, msg.Alt)
	case tea.KeyCtrlUnderscore:
		s.sendRawCtrl(0x1f, msg.Alt)
	default:
		if msg.Type >= tea.KeyCtrlA && msg.Type <= tea.KeyCtrlZ {
			ch := rune('a' + (msg.Type - tea.KeyCtrlA))
			s.emu.SendKey(uv.KeyPressEvent{Code: ch, Mod: mod | vt.ModCtrl})
		}
	}
}

func (s *TermSession) sendRawCtrl(b byte, alt bool) {
	var payload []byte
	if alt {
		payload = []byte{0x1b, b}
	} else {
		payload = []byte{b}
	}
	select {
	case s.stdinCh <- payload:
	default:
	}
}

// PasteText injects clipboard text into the PTY. Bracketed-paste markers are
// added automatically when the remote program enabled bracketed paste mode.
func (s *TermSession) PasteText(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emu == nil || s.closed {
		return
	}
	s.emu.Paste(text)
}

// SelectedText returns the plain text covered by sel on the live PTY screen.
func (s *TermSession) SelectedText(sel ScreenSelection) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.emu == nil || !sel.Active {
		return ""
	}
	w, h := s.emu.Width(), s.emu.Height()
	x0, y0, x1, y1 := sel.bounds(w, h)
	if x1 < x0 || y1 < y0 {
		return ""
	}

	lines := make([]string, 0, y1-y0+1)
	for y := y0; y <= y1; y++ {
		var b strings.Builder
		for x := x0; x <= x1; x++ {
			if c := s.emu.CellAt(x, y); c != nil {
				b.WriteString(c.Content)
			} else {
				b.WriteByte(' ')
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(lines, "\n")
}

// Render returns the ANSI-styled screen frame at scrollOffset lines above live bottom,
// along with the clamped scrollOffset and total scrollback length.
func (s *TermSession) Render(scrollOffset int, showCursor bool) (string, int, int) {
	return s.RenderWithSelection(scrollOffset, showCursor, ScreenSelection{})
}

// RenderWithSelection behaves like Render and additionally highlights the
// selected cells (if any) so mouse selection is visible to the user.
func (s *TermSession) RenderWithSelection(scrollOffset int, showCursor bool, sel ScreenSelection) (string, int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.emu == nil {
		return "", 0, 0
	}

	h := s.emu.Height()
	w := s.emu.Width()
	sb := s.emu.Scrollback()
	sbLen := 0
	if sb != nil && !s.emu.IsAltScreen() {
		sbLen = sb.Len()
	}

	if scrollOffset < 0 {
		scrollOffset = 0
	}
	if scrollOffset > sbLen {
		scrollOffset = sbLen
	}

	liveRendered := strings.ReplaceAll(s.emu.Render(), "\r\n", "\n")
	liveLines := strings.Split(liveRendered, "\n")
	for len(liveLines) < h {
		liveLines = append(liveLines, "")
	}

	if scrollOffset == 0 {
		if sel.Active {
			x0, y0, x1, y1 := sel.bounds(w, h)
			for y := y0; y <= y1 && y < len(liveLines); y++ {
				liveLines[y] = s.renderSelectionLine(y, x0, x1, w)
			}
		}
		if showCursor && !s.emu.CursorHidden() {
			pos := s.emu.CursorPosition()
			if pos.Y >= 0 && pos.Y < len(liveLines) && pos.X >= 0 && pos.X < w {
				liveLines[pos.Y] = s.renderCursorLine(pos.Y, pos.X, w)
			}
		}
		return strings.Join(liveLines[:h], "\n"), 0, sbLen
	}

	startGlobal := sbLen - scrollOffset
	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		gIdx := startGlobal + i
		if gIdx < sbLen && sb != nil {
			if line := sb.Line(gIdx); line != nil {
				out = append(out, line.Render())
			} else {
				out = append(out, "")
			}
		} else {
			ly := gIdx - sbLen
			if ly >= 0 && ly < len(liveLines) {
				out = append(out, liveLines[ly])
			} else {
				out = append(out, "")
			}
		}
	}
	return strings.Join(out, "\n"), scrollOffset, sbLen
}

func (s *TermSession) renderCursorLine(y, curX, width int) string {
	line := make(uv.Line, width)
	for x := 0; x < width; x++ {
		if c := s.emu.CellAt(x, y); c != nil && !c.IsZero() {
			line[x] = *c.Clone()
		} else {
			line[x] = uv.EmptyCell
		}
	}
	if curX >= 0 && curX < width {
		c := line[curX]
		if c.Content == "" {
			c.Content = " "
			c.Width = 1
		}
		c.Style.Attrs ^= uv.AttrReverse
		line[curX] = c
	}
	return line.Render()
}

func (s *TermSession) renderSelectionLine(y, x0, x1, width int) string {
	line := make(uv.Line, width)
	for x := 0; x < width; x++ {
		if c := s.emu.CellAt(x, y); c != nil && !c.IsZero() {
			line[x] = *c.Clone()
		} else {
			line[x] = uv.EmptyCell
		}
		if x >= x0 && x <= x1 {
			c := line[x]
			if c.Content == "" {
				c.Content = " "
				c.Width = 1
			}
			c.Style.Attrs ^= uv.AttrReverse
			line[x] = c
		}
	}
	return line.Render()
}

// Close cancels background loops, closes the VT pipe and SSH transport, waits for all
// goroutines to exit, and frees the VT emulator memory.
func (s *TermSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		emu := s.emu
		sess := s.session
		cli := s.client
		stdin := s.stdin
		tf := s.transcript
		s.mu.Unlock()

		s.cancel()
		if stdin != nil {
			_ = stdin.Close()
		}
		if emu != nil {
			if c, ok := emu.InputPipe().(io.Closer); ok {
				_ = c.Close()
			}
		}
		if sess != nil {
			_ = sess.Close()
		}
		if cli != nil {
			_ = cli.Close()
		}
		if tf != nil {
			_ = tf.Close()
		}

		s.wg.Wait()

		if emu != nil {
			_ = emu.Close()
		}

		s.mu.Lock()
		s.emu = nil
		s.session = nil
		s.client = nil
		s.transcript = nil
		s.mu.Unlock()
	})
	return nil
}
