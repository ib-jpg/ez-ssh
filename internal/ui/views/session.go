package views

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/clipboard"
	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ssh"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

// SessionState identifies the lifecycle phase of an embedded SSH session tab.
type SessionState int

const (
	StateConnecting SessionState = iota
	StateAuthFailed
	StateError
	StateConnected
	StateDisconnected
)

// SessionStepMsg notifies SessionTab of progress text updates if emitted.
type SessionConnectResultMsg struct {
	TabID      int64
	AttemptID  int64
	Session    *ssh.TermSession
	UsedSecret string
	Err        error
}

// SessionDirtyMsg signals new terminal output arrived for TabID.
type SessionDirtyMsg struct {
	TabID int64
}

// SessionExitedMsg signals the remote SSH shell exited.
type SessionExitedMsg struct {
	TabID int64
	Err   error
}

// CloseSessionTabMsg requests AppModel to close and dispose TabID.
type CloseSessionTabMsg struct {
	TabID int64
}

// EditHostFromSessionMsg switches to Vaults -> Hosts and opens the edit modal for HostID.
type EditHostFromSessionMsg struct {
	HostID int64
}

const (
	sessBtnCancel = 100 + iota
	sessBtnRetry
	sessBtnEdit
	sessBtnClose
	sessBtnSaveToggle
	sessBtnReconnect
)

// SessionTab manages one embedded SSH terminal tab and its connection state machine.
type SessionTab struct {
	ID             int64
	Host           db.Host
	Keychain       *db.KeychainItem
	store          *db.Store
	vault          *crypto.Vault
	knownHostsPath string
	logsDir        string

	State     SessionState
	width     int
	height    int
	attemptID int64

	// Step progress guarded by stepMu
	stepMu   sync.Mutex
	stepText string

	spinner       spinner.Model
	connectCtx    context.Context
	connectCancel context.CancelFunc

	term         *ssh.TermSession
	ioCtx        context.Context
	ioCancel     context.CancelFunc
	scrollOffset int
	sel          ssh.ScreenSelection
	startedAt    time.Time
	lastDuration time.Duration
	loggedRun    bool

	errMsg    string
	errFocus  int // StateError: 0=Retry, 1=Edit Host, 2=Close Tab
	authFocus int // StateAuthFailed: 0=Password, 1=Save toggle, 2=Reconnect, 3=Close
	saveVault bool
	passInput textinput.Model

	hitRects []rect
}

// NewSessionTab initializes a new embedded SSH terminal tab and returns its initial connect Cmd.
func NewSessionTab(
	id int64,
	host db.Host,
	kc *db.KeychainItem,
	store *db.Store,
	vault *crypto.Vault,
	knownHostsPath string,
	logsDir string,
	w, h int,
) *SessionTab {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(theme.Yellow).Background(theme.Base)

	pi := NewStyledInput("enter password...", 256)
	pi.EchoMode = textinput.EchoPassword
	pi.EchoCharacter = '•'

	if w < 20 {
		w = 80
	}
	if h < 6 {
		h = 24
	}

	return &SessionTab{
		ID:             id,
		Host:           host,
		Keychain:       kc,
		store:          store,
		vault:          vault,
		knownHostsPath: knownHostsPath,
		logsDir:        logsDir,
		State:          StateConnecting,
		width:          w,
		height:         h,
		stepText:       "Initializing...",
		spinner:        sp,
		saveVault:      true,
		passInput:      pi,
	}
}

// Start begins connecting to the target host asynchronously.
func (s *SessionTab) Start() tea.Cmd {
	return s.startConnect("")
}

func (s *SessionTab) startConnect(overrideSecret string) tea.Cmd {
	s.cancelConnect()
	s.closeTerm()

	s.attemptID++
	attempt := s.attemptID
	s.State = StateConnecting
	s.errMsg = ""
	s.loggedRun = false
	s.scrollOffset = 0
	s.setStep("TCP Handshake")

	ctx, cancel := context.WithCancel(context.Background())
	s.connectCtx = ctx
	s.connectCancel = cancel

	host := s.Host
	kc := s.Keychain
	vault := s.vault
	khPath := s.knownHostsPath
	logsDir := s.logsDir
	cols, rows := s.width, s.height
	tabID := s.ID

	connectCmd := func() tea.Msg {
		ts, err := ssh.StartTermSession(
			ctx, host, kc, vault, khPath, logsDir, cols, rows, overrideSecret,
			func(step string) { s.setStep(step) },
		)
		return SessionConnectResultMsg{
			TabID:      tabID,
			AttemptID:  attempt,
			Session:    ts,
			UsedSecret: overrideSecret,
			Err:        err,
		}
	}

	return tea.Batch(s.spinner.Tick, connectCmd)
}

func (s *SessionTab) setStep(step string) {
	s.stepMu.Lock()
	s.stepText = step
	s.stepMu.Unlock()
}

func (s *SessionTab) currentStep() string {
	s.stepMu.Lock()
	defer s.stepMu.Unlock()
	return s.stepText
}

// SetSize updates the viewport size and forwards PTY resize if connected.
func (s *SessionTab) SetSize(w, h int) {
	if w < 10 {
		w = 10
	}
	if h < 4 {
		h = 4
	}
	s.width = w
	s.height = h
	if s.State == StateConnected && s.term != nil {
		s.term.Resize(w, h)
	}
}

// IsSpinnerActive reports whether this tab needs periodic spinner ticks.
func (s *SessionTab) IsSpinnerActive() bool {
	return s.State == StateConnecting
}

func (s *SessionTab) cancelConnect() {
	if s.connectCancel != nil {
		s.connectCancel()
		s.connectCancel = nil
	}
}

func (s *SessionTab) closeTerm() {
	if s.ioCancel != nil {
		s.ioCancel()
		s.ioCancel = nil
	}
	if s.term != nil {
		_ = s.term.Close()
		s.term = nil
	}
}

// Close cancels any active connection, logs active session end if needed, and frees memory.
func (s *SessionTab) Close() {
	s.cancelConnect()
	if s.State == StateConnected && !s.loggedRun {
		s.recordLog(db.LogStatusSuccess, int(time.Since(s.startedAt).Seconds()), s.transcriptPath())
	}
	s.closeTerm()
}

func (s *SessionTab) transcriptPath() string {
	if s.term != nil {
		return s.term.TranscriptPath()
	}
	return ""
}

func (s *SessionTab) recordLog(status string, durSec int, tPath string) {
	if s.store == nil || s.loggedRun {
		return
	}
	if durSec < 0 {
		durSec = 0
	}
	s.loggedRun = true
	hid := s.Host.ID
	_ = s.store.CreateLog(&db.ConnectionLog{
		HostID:         &hid,
		HostName:       s.Host.Name,
		TargetAddr:     fmt.Sprintf("%s@%s:%d", s.Host.Username, s.Host.Hostname, s.Host.Port),
		Status:         status,
		DurationSec:    durSec,
		TranscriptPath: tPath,
		StartedAt:      s.startedAt,
	})
	if status == db.LogStatusSuccess {
		_ = s.store.MarkHostConnected(s.Host.ID, s.startedAt)
	}
}

// Update handles Bubble Tea messages targeted at this SessionTab.
func (s *SessionTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case spinner.TickMsg:
		if s.State != StateConnecting {
			return nil
		}
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(m)
		return cmd

	case SessionConnectResultMsg:
		if m.TabID != s.ID || m.AttemptID != s.attemptID {
			if m.Session != nil {
				_ = m.Session.Close()
			}
			return nil
		}
		if m.Err != nil {
			s.startedAt = time.Now().UTC()
			s.recordLog(db.LogStatusFailed, 0, "")
			isAuth, summary := ssh.ClassifySSHError(m.Err)
			s.errMsg = summary
			if isAuth {
				s.State = StateAuthFailed
				s.authFocus = 0
				s.passInput.SetValue("")
				s.passInput.Focus()
			} else {
				s.State = StateError
				s.errFocus = 0
			}
			return func() tea.Msg { return DataChangedMsg{} }
		}

		s.State = StateConnected
		s.term = m.Session
		s.startedAt = time.Now().UTC()
		s.scrollOffset = 0
		s.sel = ssh.ScreenSelection{}
		s.term.Resize(s.width, s.height)

		ioCtx, ioCancel := context.WithCancel(context.Background())
		s.ioCtx = ioCtx
		s.ioCancel = ioCancel

		var cmds []tea.Cmd
		cmds = append(cmds,
			waitSessionDirty(s.ID, s.term.Dirty(), ioCtx),
			waitSessionDone(s.ID, s.term.Done(), ioCtx),
		)

		cmds = append(cmds, toastCmd(components.ToastInfo, "Connected • drag mouse to select/copy • Alt+C copy all • Ctrl+V paste"))

		// Save updated password in vault if user opted in on auth retry.
		if m.UsedSecret != "" && s.saveVault {
			if err := s.persistSecret(m.UsedSecret); err == nil {
				cmds = append(cmds,
					func() tea.Msg { return DataChangedMsg{} },
					toastCmd(components.ToastSuccess, "Updated saved credential in vault"),
				)
			}
		}
		return tea.Batch(cmds...)

	case SessionDirtyMsg:
		if m.TabID != s.ID || s.State != StateConnected || s.term == nil {
			return nil
		}
		return waitSessionDirty(s.ID, s.term.Dirty(), s.ioCtx)

	case SessionExitedMsg:
		if m.TabID != s.ID || s.State != StateConnected {
			return nil
		}
		s.lastDuration = time.Since(s.startedAt)
		durSec := int(s.lastDuration.Seconds())
		tPath := s.transcriptPath()
		s.closeTerm()
		s.State = StateDisconnected
		status := db.LogStatusSuccess
		if m.Err != nil && durSec < 2 {
			status = db.LogStatusFailed
		}
		s.recordLog(status, durSec, tPath)
		return func() tea.Msg { return DataChangedMsg{} }

	case tea.MouseMsg:
		return s.handleMouse(m)

	case tea.KeyMsg:
		return s.handleKey(m)
	}
	return nil
}

func (s *SessionTab) persistSecret(secret string) error {
	if s.vault == nil || s.store == nil || secret == "" {
		return nil
	}
	enc, err := s.vault.Encrypt(secret)
	if err != nil {
		return err
	}
	if s.Keychain != nil {
		s.Keychain.SecretEnc = enc
		return s.store.UpdateKeychain(s.Keychain)
	}
	s.Host.InlineSecretEnc = enc
	return s.store.UpdateHost(&s.Host)
}

func (s *SessionTab) handleMouse(m tea.MouseMsg) tea.Cmd {
	if s.State == StateConnected {
		switch {
		case m.Action == tea.MouseActionPress && m.Button == tea.MouseButtonLeft:
			// Start a live-screen text selection. Bubble Tea owns mouse input
			// in the TUI, so native terminal selection is not possible here.
			s.scrollOffset = 0
			s.sel = ssh.ScreenSelection{
				Active: true,
				X0:     m.X, Y0: m.Y,
				X1: m.X, Y1: m.Y,
			}
			return nil
		case m.Action == tea.MouseActionMotion && m.Button == tea.MouseButtonLeft && s.sel.Active:
			s.sel.X1, s.sel.Y1 = m.X, m.Y
			return nil
		case m.Action == tea.MouseActionRelease && s.sel.Active:
			s.sel.X1, s.sel.Y1 = m.X, m.Y
			return s.copySelection()
		case m.Button == tea.MouseButtonWheelUp:
			s.scrollOffset += 3
			return nil
		case m.Button == tea.MouseButtonWheelDown:
			s.scrollOffset -= 3
			if s.scrollOffset < 0 {
				s.scrollOffset = 0
			}
			return nil
		}
		return nil
	}

	if m.Action != tea.MouseActionPress || m.Button != tea.MouseButtonLeft {
		return nil
	}

	for _, r := range s.hitRects {
		if r.contains(m.X, m.Y) {
			switch r.id {
			case sessBtnCancel, sessBtnClose:
				s.Close()
				return func() tea.Msg { return CloseSessionTabMsg{TabID: s.ID} }
			case sessBtnRetry:
				return s.startConnect("")
			case sessBtnEdit:
				id := s.Host.ID
				return func() tea.Msg { return EditHostFromSessionMsg{HostID: id} }
			case sessBtnSaveToggle:
				s.saveVault = !s.saveVault
				return nil
			case sessBtnReconnect:
				return s.submitAuthRetry()
			}
		}
	}
	return nil
}

func (s *SessionTab) handleKey(m tea.KeyMsg) tea.Cmd {
	switch s.State {
	case StateConnecting:
		if m.String() == "esc" || m.String() == "enter" {
			s.Close()
			return func() tea.Msg { return CloseSessionTabMsg{TabID: s.ID} }
		}
		return nil

	case StateAuthFailed:
		switch m.String() {
		case "esc":
			s.Close()
			return func() tea.Msg { return CloseSessionTabMsg{TabID: s.ID} }
		case "tab", "down":
			s.authFocus = (s.authFocus + 1) % 4
			s.syncAuthFocus()
			return nil
		case "shift+tab", "up":
			s.authFocus = (s.authFocus + 3) % 4
			s.syncAuthFocus()
			return nil
		case "enter":
			switch s.authFocus {
			case 1:
				s.saveVault = !s.saveVault
				return nil
			case 3:
				s.Close()
				return func() tea.Msg { return CloseSessionTabMsg{TabID: s.ID} }
			default:
				return s.submitAuthRetry()
			}
		case " ":
			if s.authFocus == 1 {
				s.saveVault = !s.saveVault
				return nil
			}
		}
		if s.authFocus == 0 {
			var cmd tea.Cmd
			s.passInput, cmd = s.passInput.Update(m)
			return cmd
		}
		return nil

	case StateError:
		switch m.String() {
		case "esc":
			s.Close()
			return func() tea.Msg { return CloseSessionTabMsg{TabID: s.ID} }
		case "r":
			return s.startConnect("")
		case "e":
			id := s.Host.ID
			return func() tea.Msg { return EditHostFromSessionMsg{HostID: id} }
		case "left", "h":
			s.errFocus = (s.errFocus + 2) % 3
		case "right", "l", "tab":
			s.errFocus = (s.errFocus + 1) % 3
		case "enter", " ":
			switch s.errFocus {
			case 0:
				return s.startConnect("")
			case 1:
				id := s.Host.ID
				return func() tea.Msg { return EditHostFromSessionMsg{HostID: id} }
			case 2:
				s.Close()
				return func() tea.Msg { return CloseSessionTabMsg{TabID: s.ID} }
			}
		}
		return nil

	case StateDisconnected:
		switch m.String() {
		case "esc", "q":
			s.Close()
			return func() tea.Msg { return CloseSessionTabMsg{TabID: s.ID} }
		case "r", "enter":
			return s.startConnect("")
		}
		return nil

	case StateConnected:
		if s.term == nil {
			return nil
		}
		switch m.String() {
		case "alt+c":
			// Copy the visible terminal screen when no mouse selection was made.
			return s.copyScreen()
		case "ctrl+v", "alt+v":
			return s.pasteClipboard()
		case "shift+pgup":
			s.scrollOffset += maxInt(1, s.height/2)
			return nil
		case "shift+pgdown":
			s.scrollOffset -= maxInt(1, s.height/2)
			if s.scrollOffset < 0 {
				s.scrollOffset = 0
			}
			return nil
		}
		s.scrollOffset = 0
		s.term.WriteKey(m)
		return nil
	}
	return nil
}

func (s *SessionTab) syncAuthFocus() {
	if s.authFocus == 0 {
		s.passInput.Focus()
	} else {
		s.passInput.Blur()
	}
}

func (s *SessionTab) submitAuthRetry() tea.Cmd {
	pw := s.passInput.Value()
	if pw == "" {
		s.errMsg = "Please enter a password or passphrase to reconnect."
		return nil
	}
	return s.startConnect(pw)
}

func (s *SessionTab) copySelection() tea.Cmd {
	if s.term == nil || !s.sel.Active {
		return nil
	}
	text := s.term.SelectedText(s.sel)
	s.sel = ssh.ScreenSelection{}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return s.copyText(text)
}

func (s *SessionTab) copyScreen() tea.Cmd {
	if s.term == nil {
		return nil
	}
	s.sel = ssh.ScreenSelection{}
	frame, _, _ := s.term.Render(0, false)
	plain := components.SanitizeTranscript([]byte(frame))
	lines := strings.Split(plain, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return s.copyText(strings.TrimRight(strings.Join(lines, "\n"), "\n"))
}

func (s *SessionTab) copyText(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return toastCmd(components.ToastInfo, "Nothing to copy")
	}
	via, err := clipboard.Write(text)
	if err != nil {
		return toastCmd(components.ToastError, "Copy failed: "+err.Error())
	}
	lineCount := strings.Count(text, "\n") + 1
	return toastCmd(components.ToastSuccess, fmt.Sprintf("Copied %d line(s) to clipboard (%s)", lineCount, via))
}

func (s *SessionTab) pasteClipboard() tea.Cmd {
	if s.term == nil || s.State != StateConnected {
		return nil
	}
	text, err := clipboard.Read()
	if err != nil || text == "" {
		return toastCmd(components.ToastError, "Paste failed: clipboard empty or unavailable")
	}
	s.term.PasteText(text)
	s.scrollOffset = 0
	return toastCmd(components.ToastSuccess, "Pasted from clipboard")
}

// View renders the session tab inside (w, h) and records clickable button coordinates.
func (s *SessionTab) View(w, h int) string {
	s.hitRects = s.hitRects[:0]
	if w <= 0 || h <= 0 {
		return ""
	}

	switch s.State {
	case StateConnected:
		if s.term == nil {
			return ""
		}
		frame, clamped, _ := s.term.RenderWithSelection(s.scrollOffset, true, s.sel)
		s.scrollOffset = clamped
		if clamped > 0 {
			badge := theme.ActionPill.Render(fmt.Sprintf("Scrollback -%dL (Shift+PgDn)", clamped))
			frame = components.OverlayTopRight(frame, badge, w)
		}
		return frame

	case StateConnecting:
		return s.renderConnectingCard(w, h)
	case StateAuthFailed:
		return s.renderAuthFailedCard(w, h)
	case StateError:
		return s.renderErrorCard(w, h)
	case StateDisconnected:
		return s.renderDisconnectedCard(w, h)
	default:
		return ""
	}
}

func (s *SessionTab) renderCardCentered(w, h, maxCardW int, borderColor lipgloss.Color, lines []string, btnRow string, btns []struct {
	id    int
	label string
	style lipgloss.Style
}) string {
	cardW := minInt(maxCardW, maxInt(28, w-4))
	innerW := cardW - 6 // border(2) + padding(4)

	txtBase := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base)
	bodyLines := make([]string, 0, len(lines)+2)
	for _, l := range lines {
		if l == "" {
			bodyLines = append(bodyLines, theme.SpaceBg(theme.Base, innerW))
			continue
		}
		if !strings.Contains(l, "\x1b[") {
			l = txtBase.Render(l)
		}
		bodyLines = append(bodyLines, padBgLine(l, innerW, theme.Base))
	}

	var renderedBtns []string
	var btnWidths []int
	for _, b := range btns {
		r := b.style.Render(b.label)
		renderedBtns = append(renderedBtns, r)
		btnWidths = append(btnWidths, lipgloss.Width(r))
	}
	if btnRow == "" && len(renderedBtns) > 0 {
		btnRow = strings.Join(renderedBtns, theme.SpaceBg(theme.Base, 2))
	}

	content := strings.Join(bodyLines, "\n")
	if btnRow != "" {
		content += "\n" + theme.SpaceBg(theme.Base, innerW) + "\n" + padBgLine(btnRow, innerW, theme.Base)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		BorderBackground(theme.Crust).
		Background(theme.Base).
		Foreground(theme.Text).
		Padding(1, 2).
		Width(cardW - 2).
		MaxWidth(w).
		Render(content)

	boxW := lipgloss.Width(box)
	boxH := lipgloss.Height(box)
	boxX0 := maxInt(0, (w-boxW)/2)
	boxY0 := maxInt(0, (h-boxH)/2)

	// Record hitboxes for buttons on the last line inside padding/border (boxY0 + boxH - 3).
	if len(btns) > 0 {
		btnY := boxY0 + boxH - 3
		curX := boxX0 + 3 // border(1) + padding(2)
		for i, b := range btns {
			bw := btnWidths[i]
			s.hitRects = append(s.hitRects, rect{
				id: b.id,
				x0: curX,
				y0: btnY,
				x1: curX + bw,
				y1: btnY + 1,
			})
			curX += bw + 2
		}
	}

	return lipgloss.Place(
		w, h,
		lipgloss.Center, lipgloss.Center,
		box,
		lipgloss.WithWhitespaceBackground(theme.Crust),
	)
}

func (s *SessionTab) renderConnectingCard(w, h int) string {
	osInfo := theme.LookupOS(s.Host.OSType)
	osIcon := lipgloss.NewStyle().Foreground(osInfo.Color).Background(theme.Base).Bold(true).Render(osInfo.Icon)
	txtBase := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base)

	lines := []string{
		s.spinner.View() + txtBase.Render(fmt.Sprintf(" Connecting to %s (%s:%d)...", s.Host.Name, s.Host.Hostname, s.Host.Port)),
		"",
		osIcon + txtBase.Render(fmt.Sprintf("  %s@%s:%d", s.Host.Username, s.Host.Hostname, s.Host.Port)),
		theme.InspectorLabel.Background(theme.Base).Render("Stage: ") + theme.InspectorValue.Background(theme.Base).Render(s.currentStep()),
	}

	btns := []struct {
		id    int
		label string
		style lipgloss.Style
	}{
		{sessBtnCancel, "Cancel (Esc)", theme.ButtonInactive},
	}
	return s.renderCardCentered(w, h, 62, theme.Yellow, lines, "", btns)
}

func (s *SessionTab) renderAuthFailedCard(w, h int) string {
	cardW := minInt(66, maxInt(32, w-4))
	innerW := cardW - 6

	passBox := RenderRoundedInputBox(&s.passInput, innerW, s.authFocus == 0, theme.Mantle, theme.Base)

	check := "[ ] Update saved password in vault"
	if s.saveVault {
		check = "[✓] Update saved password in vault"
	}
	checkStyle := theme.InspectorLabel.Background(theme.Base)
	if s.authFocus == 1 {
		checkStyle = theme.ActionPill
	}
	checkLine := checkStyle.Render(check)

	txtBase := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base)
	lines := []string{
		theme.StatusError.Render("✖ Authentication Failed"),
		txtBase.Render(fmt.Sprintf("Incorrect password for %s@%s:%d.", s.Host.Username, s.Host.Hostname, s.Host.Port)),
	}
	if s.errMsg != "" {
		lines = append(lines, theme.InspectorLabel.Background(theme.Base).Render(s.errMsg))
	}
	lines = append(lines, "")
	lines = append(lines, strings.Split(passBox, "\n")...)
	lines = append(lines, checkLine)

	recStyle := theme.ButtonInactive
	if s.authFocus == 2 {
		recStyle = theme.ButtonActive
	}
	clsStyle := theme.ButtonInactive
	if s.authFocus == 3 {
		clsStyle = theme.ButtonDanger
	}
	btns := []struct {
		id    int
		label string
		style lipgloss.Style
	}{
		{sessBtnReconnect, "Reconnect (Enter)", recStyle},
		{sessBtnClose, "Close Tab (Esc)", clsStyle},
	}

	out := s.renderCardCentered(w, h, 66, theme.Red, lines, "", btns)

	// Also register clickable hitbox for the saveVault toggle line (2 rows above buttons).
	boxH := len(lines) + 6
	boxY0 := maxInt(0, (h-boxH)/2)
	boxX0 := maxInt(0, (w-cardW)/2)
	toggleY := boxY0 + boxH - 5
	s.hitRects = append(s.hitRects, rect{
		id: sessBtnSaveToggle,
		x0: boxX0 + 3,
		y0: toggleY,
		x1: boxX0 + 3 + lipgloss.Width(checkLine),
		y1: toggleY + 1,
	})
	return out
}

func (s *SessionTab) renderErrorCard(w, h int) string {
	osInfo := theme.LookupOS(s.Host.OSType)
	osIcon := lipgloss.NewStyle().Foreground(osInfo.Color).Background(theme.Base).Bold(true).Render(osInfo.Icon)
	txtBase := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base)

	lines := []string{
		theme.StatusError.Render("✖ Connection Failed"),
		osIcon + txtBase.Render(fmt.Sprintf("  %s (%s@%s:%d)", s.Host.Name, s.Host.Username, s.Host.Hostname, s.Host.Port)),
		"",
		theme.InspectorValue.Background(theme.Base).Render(s.errMsg),
	}

	retryStyle := theme.ButtonInactive
	editStyle := theme.ButtonInactive
	closeStyle := theme.ButtonInactive
	switch s.errFocus {
	case 0:
		retryStyle = theme.ButtonActive
	case 1:
		editStyle = theme.ButtonActive
	case 2:
		closeStyle = theme.ButtonDanger
	}

	btns := []struct {
		id    int
		label string
		style lipgloss.Style
	}{
		{sessBtnRetry, "↻ Retry (r)", retryStyle},
		{sessBtnEdit, "✎ Edit Host (e)", editStyle},
		{sessBtnClose, "× Close (Esc)", closeStyle},
	}
	return s.renderCardCentered(w, h, 68, theme.Red, lines, "", btns)
}

func (s *SessionTab) renderDisconnectedCard(w, h int) string {
	lines := []string{
		theme.InspectorTitle.Background(theme.Base).Render("Session Disconnected"),
		fmt.Sprintf("%s (%s@%s:%d)", s.Host.Name, s.Host.Username, s.Host.Hostname, s.Host.Port),
		theme.InspectorLabel.Background(theme.Base).Render("Duration: ") + theme.InspectorValue.Background(theme.Base).Render(HumanDuration(int(s.lastDuration.Seconds()))),
	}
	btns := []struct {
		id    int
		label string
		style lipgloss.Style
	}{
		{sessBtnRetry, "↻ Reconnect (r)", theme.ButtonActive},
		{sessBtnClose, "× Close Tab (Esc)", theme.ButtonInactive},
	}
	return s.renderCardCentered(w, h, 58, theme.Surface1, lines, "", btns)
}

func waitSessionDirty(tabID int64, ch <-chan struct{}, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			return SessionDirtyMsg{TabID: tabID}
		}
	}
}

func waitSessionDone(tabID int64, ch <-chan error, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-ch:
			if !ok {
				return nil
			}
			return SessionExitedMsg{TabID: tabID, Err: err}
		}
	}
}
