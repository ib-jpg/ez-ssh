package views

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ssh"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

const (
	paneLocal  = 0
	paneRemote = 1
)

// SFTP async messages
type (
	SFTPConnectResultMsg struct {
		Host      db.Host
		Client    *ssh.SFTPClient
		RemoteDir string
		Entries   []ssh.FileEntry
		Err       error
	}

	SFTPListResultMsg struct {
		RemoteDir string
		Entries   []ssh.FileEntry
		Err       error
	}

	SFTPTransferProgressMsg struct {
		Progress ssh.TransferProgress
	}

	SFTPTransferDoneMsg struct {
		Action string
		Err    error
	}
)

// SFTPView implements the dual-pane SFTP file manager workspace.
type SFTPView struct {
	store          *db.Store
	vault          *crypto.Vault
	knownHostsPath string

	width      int
	height     int
	activePane int
	showHidden bool

	// Local pane state
	localDir     string
	localEntries []ssh.FileEntry
	localCursor  int
	localOffset  int

	// Remote pane state
	client        *ssh.SFTPClient
	remoteHost    *db.Host
	connecting    bool
	connSpinner   spinner.Model
	connCancel    context.CancelFunc
	remoteDir     string
	remoteEntries []ssh.FileEntry
	remoteCursor  int
	remoteOffset  int

	// Transfer state
	transferring   bool
	transferAction string
	transferProg   ssh.TransferProgress
	transferCtx    context.Context
	transferCancel context.CancelFunc
	progCh         chan ssh.TransferProgress

	// Modals
	pickHostOpen bool
	pickHosts    []db.Host
	pickCursor   int

	inputModalKind string // "" | "mkdir" | "rename"
	inputField     textinput.Model
	renameTarget   ssh.FileEntry
	formErr        string

	confirm      components.ConfirmModel
	deleteTarget ssh.FileEntry
	deletePane   int

	// Click hitboxes relative to SFTPView(0,0)
	selectHostBtnRect   rect
	emptyConnectBtnRect rect
	refreshBtnRect      rect
	hiddenBtnRect       rect
	uploadBtnRect       rect
	downloadBtnRect     rect
	transferRowVisible  bool
	lastClickTime       time.Time
	lastClickPane       int
	lastClickIdx        int

	// In-app drag & drop between the two panes. Terminals do not forward
	// desktop file drops to a TUI, but cell-motion mode does deliver drag
	// events, so a file can be dragged from one pane and dropped in the other.
	dragCandidate bool
	dragging      bool
	dragPane      int
	dragEntry     ssh.FileEntry
	dragStartX    int
	dragStartY    int
	dragCurX      int
	dragCurY      int
}

// NewSFTPView constructs a dual-pane SFTP workspace defaulting local pane to $HOME.
func NewSFTPView(store *db.Store, vault *crypto.Vault, knownHostsPath string) *SFTPView {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(theme.Yellow).Background(theme.Base)

	v := &SFTPView{
		store:          store,
		vault:          vault,
		knownHostsPath: knownHostsPath,
		localDir:       home,
		connSpinner:    sp,
		inputField:     NewStyledInput("name...", 128),
	}
	v.refreshLocal()
	return v
}

func (v *SFTPView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// SetLocalDir sets the starting local directory (useful in tests).
func (v *SFTPView) SetLocalDir(dir string) {
	v.localDir = dir
	v.localCursor = 0
	v.localOffset = 0
	v.refreshLocal()
}

// ActivePane returns 0 for local pane, 1 for remote pane.
func (v *SFTPView) ActivePane() int {
	return v.activePane
}

// ShowHidden reports whether hidden dotfiles are displayed.
func (v *SFTPView) ShowHidden() bool {
	return v.showHidden
}

// LocalEntriesCount returns number of visible local entries.
func (v *SFTPView) LocalEntriesCount() int {
	return len(v.localEntries)
}

func (v *SFTPView) IsModalOpen() bool {
	return v.pickHostOpen || v.inputModalKind != "" || v.confirm.Active
}

func (v *SFTPView) IsSpinnerActive() bool {
	return v.connecting
}

// Close cancels any active connection/transfer and closes the SFTP client.
func (v *SFTPView) Close() {
	if v.connCancel != nil {
		v.connCancel()
		v.connCancel = nil
	}
	if v.transferCancel != nil {
		v.transferCancel()
		v.transferCancel = nil
	}
	if v.client != nil {
		_ = v.client.Close()
		v.client = nil
	}
	v.connecting = false
	v.transferring = false
	v.remoteHost = nil
	v.remoteEntries = nil
	v.clearDrag()
}

// ConnectHost initiates an SFTP connection to host and switches focus to the remote pane.
func (v *SFTPView) ConnectHost(host db.Host, kc *db.KeychainItem) tea.Cmd {
	v.Close()
	v.connecting = true
	hCopy := host
	v.remoteHost = &hCopy
	v.activePane = paneRemote

	ctx, cancel := context.WithCancel(context.Background())
	v.connCancel = cancel

	vault := v.vault
	khPath := v.knownHostsPath
	showHidden := v.showHidden

	connectCmd := func() tea.Msg {
		cli, err := ssh.ConnectSFTP(ctx, host, kc, vault, khPath)
		if err != nil {
			return SFTPConnectResultMsg{Host: host, Err: err}
		}
		dir, entries, listErr := cli.ListRemote("", showHidden)
		if listErr != nil {
			_ = cli.Close()
			return SFTPConnectResultMsg{Host: host, Err: listErr}
		}
		return SFTPConnectResultMsg{
			Host:      host,
			Client:    cli,
			RemoteDir: dir,
			Entries:   entries,
		}
	}
	return tea.Batch(v.connSpinner.Tick, connectCmd)
}

func (v *SFTPView) refreshLocal() {
	abs, entries, err := ssh.ListLocal(v.localDir, v.showHidden)
	if err == nil {
		v.localDir = abs
		v.localEntries = entries
		if v.localCursor >= len(v.localEntries) {
			v.localCursor = maxInt(0, len(v.localEntries)-1)
		}
	}
}

func (v *SFTPView) refreshRemoteCmd() tea.Cmd {
	if v.client == nil {
		return nil
	}
	cli := v.client
	dir := v.remoteDir
	showHidden := v.showHidden
	return func() tea.Msg {
		newDir, entries, err := cli.ListRemote(dir, showHidden)
		return SFTPListResultMsg{RemoteDir: newDir, Entries: entries, Err: err}
	}
}

func (v *SFTPView) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case spinner.TickMsg:
		if v.connecting {
			var cmd tea.Cmd
			v.connSpinner, cmd = v.connSpinner.Update(m)
			return cmd
		}
		return nil

	case SFTPConnectResultMsg:
		v.connecting = false
		if m.Err != nil {
			v.remoteHost = nil
			return toastCmd(components.ToastError, fmt.Sprintf("SFTP connect failed: %v", m.Err))
		}
		v.client = m.Client
		v.remoteDir = m.RemoteDir
		v.remoteEntries = m.Entries
		v.remoteCursor = 0
		v.remoteOffset = 0
		return toastCmd(components.ToastSuccess, fmt.Sprintf("SFTP connected to %s", m.Host.Name))

	case SFTPListResultMsg:
		if m.Err != nil {
			return toastCmd(components.ToastError, fmt.Sprintf("Remote list error: %v", m.Err))
		}
		v.remoteDir = m.RemoteDir
		v.remoteEntries = m.Entries
		if v.remoteCursor >= len(v.remoteEntries) {
			v.remoteCursor = maxInt(0, len(v.remoteEntries)-1)
		}
		return nil

	case SFTPTransferProgressMsg:
		if !v.transferring {
			return nil
		}
		v.transferProg = m.Progress
		return waitTransferProgress(v.progCh, v.transferCtx)

	case SFTPTransferDoneMsg:
		v.transferring = false
		if v.transferCancel != nil {
			v.transferCancel()
			v.transferCancel = nil
		}
		v.transferCtx = nil
		if v.progCh != nil {
			close(v.progCh)
			v.progCh = nil
		}
		v.refreshLocal()
		if m.Err != nil {
			return tea.Batch(
				v.refreshRemoteCmd(),
				toastCmd(components.ToastError, fmt.Sprintf("%s failed: %v", m.Action, m.Err)),
			)
		}
		return tea.Batch(
			v.refreshRemoteCmd(),
			toastCmd(components.ToastSuccess, fmt.Sprintf("%s complete", m.Action)),
		)

	case tea.MouseMsg:
		return v.handleMouse(m)

	case tea.KeyMsg:
		return v.handleKey(m)
	}
	return nil
}

func (v *SFTPView) handleMouse(m tea.MouseMsg) tea.Cmd {
	if v.IsModalOpen() {
		return nil
	}

	// Cell-motion mode reports motion events only while a button is held,
	// which is exactly what we need for pane-to-pane drag & drop.
	if m.Action == tea.MouseActionMotion {
		if v.dragCandidate {
			v.dragCurX, v.dragCurY = m.X, m.Y
			dx := m.X - v.dragStartX
			if dx < 0 {
				dx = -dx
			}
			dy := m.Y - v.dragStartY
			if dy < 0 {
				dy = -dy
			}
			if dx+dy >= 2 {
				v.dragging = true
			}
		}
		return nil
	}

	if m.Action == tea.MouseActionRelease {
		return v.finishDrag(m)
	}

	if m.Button == tea.MouseButtonWheelUp {
		v.clearDrag()
		v.moveCursor(-1)
		return nil
	}
	if m.Button == tea.MouseButtonWheelDown {
		v.clearDrag()
		v.moveCursor(1)
		return nil
	}
	if m.Action != tea.MouseActionPress || m.Button != tea.MouseButtonLeft {
		return nil
	}

	if v.transferRowVisible {
		if v.uploadBtnRect.contains(m.X, m.Y) {
			v.clearDrag()
			return v.startUpload()
		}
		if v.downloadBtnRect.contains(m.X, m.Y) {
			v.clearDrag()
			return v.startDownload()
		}
	}
	if v.selectHostBtnRect.contains(m.X, m.Y) || v.emptyConnectBtnRect.contains(m.X, m.Y) {
		v.clearDrag()
		return v.openHostPicker()
	}
	if v.hiddenBtnRect.contains(m.X, m.Y) {
		v.clearDrag()
		v.showHidden = !v.showHidden
		v.refreshLocal()
		return v.refreshRemoteCmd()
	}
	if v.refreshBtnRect.contains(m.X, m.Y) {
		v.clearDrag()
		v.refreshLocal()
		return v.refreshRemoteCmd()
	}

	pane, inside := v.paneAt(m.X, m.Y)
	if !inside {
		return nil
	}
	entries := v.localEntries
	offset := v.localOffset
	cursor := &v.localCursor
	if pane == paneRemote {
		entries = v.remoteEntries
		offset = v.remoteOffset
		cursor = &v.remoteCursor
	}
	v.activePane = pane

	rowIdx := m.Y - v.paneTop() - 4 // pane border + title + column header + separator
	if rowIdx < 0 {
		return nil
	}
	idx := offset + rowIdx
	if idx < 0 || idx >= len(entries) {
		return nil
	}

	entry := entries[idx]
	now := time.Now()
	double := now.Sub(v.lastClickTime) < 400*time.Millisecond && v.lastClickPane == pane && v.lastClickIdx == idx
	*cursor = idx
	v.lastClickTime = now
	v.lastClickPane = pane
	v.lastClickIdx = idx
	if double {
		v.clearDrag()
		return v.enterSelected()
	}

	// Arm a possible drag from this row. It only becomes a real drag once the
	// pointer moves, so plain clicks and double-clicks keep working as before.
	if !entry.IsParent {
		v.dragCandidate = true
		v.dragging = false
		v.dragPane = pane
		v.dragEntry = entry
		v.dragStartX, v.dragStartY = m.X, m.Y
		v.dragCurX, v.dragCurY = m.X, m.Y
	}
	return nil
}

// finishDrag completes a pane-to-pane drop. A drag from the local pane uploads
// the file to the remote directory; a drag from the remote pane downloads it.
func (v *SFTPView) finishDrag(m tea.MouseMsg) tea.Cmd {
	if !v.dragCandidate {
		v.clearDrag()
		return nil
	}
	srcPane := v.dragPane
	entry := v.dragEntry
	wasDragging := v.dragging
	v.clearDrag()
	if !wasDragging {
		return nil
	}
	targetPane, inside := v.paneAt(m.X, m.Y)
	if !inside || targetPane == srcPane {
		return toastCmd(components.ToastInfo, "Drop the file on the opposite pane to transfer")
	}
	v.setCursorToEntry(srcPane, entry)
	v.activePane = srcPane
	if srcPane == paneLocal {
		return v.startUpload()
	}
	return v.startDownload()
}

func (v *SFTPView) paneTop() int {
	if v.transferRowVisible {
		return 2
	}
	return 1
}

func (v *SFTPView) paneAt(x, y int) (int, bool) {
	if y < v.paneTop() {
		return 0, false
	}
	if x < v.width/2 {
		return paneLocal, true
	}
	return paneRemote, true
}

func (v *SFTPView) clearDrag() {
	v.dragCandidate = false
	v.dragging = false
	v.dragPane = 0
	v.dragEntry = ssh.FileEntry{}
	v.dragStartX, v.dragStartY = 0, 0
	v.dragCurX, v.dragCurY = 0, 0
}

func (v *SFTPView) setCursorToEntry(pane int, entry ssh.FileEntry) {
	if pane == paneLocal {
		for i, e := range v.localEntries {
			if e.FullPath == entry.FullPath {
				v.localCursor = i
				return
			}
		}
		return
	}
	for i, e := range v.remoteEntries {
		if e.FullPath == entry.FullPath {
			v.remoteCursor = i
			return
		}
	}
}

func (v *SFTPView) handleKey(m tea.KeyMsg) tea.Cmd {
	// Any keyboard interaction cancels an in-progress mouse gesture, so a
	// drag left hanging by a workspace switch cannot fire later.
	v.clearDrag()
	if v.confirm.Active {
		confirmed, closed := v.confirm.Update(m)
		if closed && confirmed {
			return v.executeDelete()
		}
		return nil
	}

	if v.pickHostOpen {
		return v.updateHostPicker(m)
	}

	if v.inputModalKind != "" {
		return v.updateInputModal(m)
	}

	if v.transferring && m.String() == "esc" {
		if v.transferCancel != nil {
			v.transferCancel()
		}
		return toastCmd(components.ToastInfo, "Cancelling transfer...")
	}

	switch m.String() {
	case "tab", "left", "right":
		if v.activePane == paneLocal {
			v.activePane = paneRemote
		} else {
			v.activePane = paneLocal
		}
	case "j", "down":
		v.moveCursor(1)
	case "k", "up":
		v.moveCursor(-1)
	case "g", "home":
		if v.activePane == paneLocal {
			v.localCursor = 0
			v.localOffset = 0
		} else {
			v.remoteCursor = 0
			v.remoteOffset = 0
		}
	case "G", "end":
		if v.activePane == paneLocal && len(v.localEntries) > 0 {
			v.localCursor = len(v.localEntries) - 1
		} else if v.activePane == paneRemote && len(v.remoteEntries) > 0 {
			v.remoteCursor = len(v.remoteEntries) - 1
		}
	case "enter":
		return v.enterSelected()
	case "backspace":
		return v.goUp()
	case ".":
		v.showHidden = !v.showHidden
		v.refreshLocal()
		return v.refreshRemoteCmd()
	case "c":
		return v.openHostPicker()
	case "D":
		v.Close()
		return toastCmd(components.ToastInfo, "SFTP disconnected")
	case "R":
		v.refreshLocal()
		return tea.Batch(v.refreshRemoteCmd(), toastCmd(components.ToastInfo, "Refreshed"))
	case "u":
		return v.startUpload()
	case "d":
		return v.startDownload()
	case "f5":
		if v.activePane == paneLocal {
			return v.startUpload()
		}
		return v.startDownload()
	case "m":
		v.openInputModal("mkdir", ssh.FileEntry{})
	case "r":
		if entry, ok := v.selectedEntry(); ok && !entry.IsParent {
			v.openInputModal("rename", entry)
		}
	case "x":
		if entry, ok := v.selectedEntry(); ok && !entry.IsParent {
			v.deleteTarget = entry
			v.deletePane = v.activePane
			v.confirm.Open("sftp-del", "Confirm Delete", fmt.Sprintf("Permanently delete %q?", entry.Name), true)
		}
	}
	return nil
}

func (v *SFTPView) moveCursor(delta int) {
	if v.activePane == paneLocal {
		if len(v.localEntries) > 0 {
			v.localCursor = clampInt(v.localCursor+delta, 0, len(v.localEntries)-1)
		}
	} else {
		if len(v.remoteEntries) > 0 {
			v.remoteCursor = clampInt(v.remoteCursor+delta, 0, len(v.remoteEntries)-1)
		}
	}
}

func (v *SFTPView) selectedEntry() (ssh.FileEntry, bool) {
	if v.activePane == paneLocal {
		if v.localCursor >= 0 && v.localCursor < len(v.localEntries) {
			return v.localEntries[v.localCursor], true
		}
		return ssh.FileEntry{}, false
	}
	if v.remoteCursor >= 0 && v.remoteCursor < len(v.remoteEntries) {
		return v.remoteEntries[v.remoteCursor], true
	}
	return ssh.FileEntry{}, false
}

func (v *SFTPView) enterSelected() tea.Cmd {
	e, ok := v.selectedEntry()
	if !ok {
		return nil
	}
	if !e.IsDir {
		return nil
	}
	if v.activePane == paneLocal {
		v.localDir = e.FullPath
		v.localCursor = 0
		v.localOffset = 0
		v.refreshLocal()
		return nil
	}
	if v.client == nil {
		return nil
	}
	v.remoteDir = e.FullPath
	v.remoteCursor = 0
	v.remoteOffset = 0
	return v.refreshRemoteCmd()
}

func (v *SFTPView) goUp() tea.Cmd {
	if v.activePane == paneLocal {
		parent := filepath.Dir(v.localDir)
		if parent != v.localDir {
			v.localDir = parent
			v.localCursor = 0
			v.localOffset = 0
			v.refreshLocal()
		}
		return nil
	}
	if v.client == nil || v.remoteDir == "/" {
		return nil
	}
	v.remoteDir = path.Dir(v.remoteDir)
	v.remoteCursor = 0
	v.remoteOffset = 0
	return v.refreshRemoteCmd()
}

func (v *SFTPView) startUpload() tea.Cmd {
	if v.client == nil {
		return toastCmd(components.ToastInfo, "Connect to a remote host first ([c])")
	}
	if v.transferring {
		return toastCmd(components.ToastInfo, "Another transfer is already in progress")
	}
	if v.localCursor < 0 || v.localCursor >= len(v.localEntries) {
		return toastCmd(components.ToastInfo, "Select a local file to upload first")
	}
	entry := v.localEntries[v.localCursor]
	if entry.IsParent {
		return toastCmd(components.ToastInfo, "Select a file or folder to upload")
	}

	ctx, cancel := context.WithCancel(context.Background())
	v.transferring = true
	v.transferAction = "Upload"
	v.transferCtx = ctx
	v.transferCancel = cancel
	v.transferProg = ssh.TransferProgress{Filename: entry.Name}
	progCh := make(chan ssh.TransferProgress, 16)
	v.progCh = progCh

	cli := v.client
	remoteDir := v.remoteDir
	uploadCmd := func() tea.Msg {
		err := cli.Upload(ctx, entry.FullPath, remoteDir, func(p ssh.TransferProgress) {
			select {
			case progCh <- p:
			default:
			}
		})
		return SFTPTransferDoneMsg{Action: "Upload " + entry.Name, Err: err}
	}
	return tea.Batch(waitTransferProgress(progCh, ctx), uploadCmd)
}

func (v *SFTPView) startDownload() tea.Cmd {
	if v.client == nil {
		return toastCmd(components.ToastInfo, "Connect to a remote host first ([c])")
	}
	if v.transferring {
		return toastCmd(components.ToastInfo, "Another transfer is already in progress")
	}
	if v.remoteCursor < 0 || v.remoteCursor >= len(v.remoteEntries) {
		return toastCmd(components.ToastInfo, "Select a remote file to download first")
	}
	entry := v.remoteEntries[v.remoteCursor]
	if entry.IsParent {
		return toastCmd(components.ToastInfo, "Select a file or folder to download")
	}

	ctx, cancel := context.WithCancel(context.Background())
	v.transferring = true
	v.transferAction = "Download"
	v.transferCtx = ctx
	v.transferCancel = cancel
	v.transferProg = ssh.TransferProgress{Filename: entry.Name}
	progCh := make(chan ssh.TransferProgress, 16)
	v.progCh = progCh

	cli := v.client
	localDir := v.localDir
	dlCmd := func() tea.Msg {
		err := cli.Download(ctx, entry.FullPath, localDir, func(p ssh.TransferProgress) {
			select {
			case progCh <- p:
			default:
			}
		})
		return SFTPTransferDoneMsg{Action: "Download " + entry.Name, Err: err}
	}
	return tea.Batch(waitTransferProgress(progCh, ctx), dlCmd)
}

func waitTransferProgress(ch <-chan ssh.TransferProgress, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if ch == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case p, ok := <-ch:
			if !ok {
				return nil
			}
			return SFTPTransferProgressMsg{Progress: p}
		}
	}
}

func (v *SFTPView) openHostPicker() tea.Cmd {
	hosts, err := v.store.ListHosts()
	if err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Load hosts: %v", err))
	}
	if len(hosts) == 0 {
		return toastCmd(components.ToastInfo, "Create an SSH Host in Vaults first")
	}
	v.pickHostOpen = true
	v.pickHosts = hosts
	v.pickCursor = 0
	return nil
}

func (v *SFTPView) updateHostPicker(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		v.pickHostOpen = false
		return nil
	case "j", "down":
		if len(v.pickHosts) > 0 {
			v.pickCursor = (v.pickCursor + 1) % len(v.pickHosts)
		}
	case "k", "up":
		if len(v.pickHosts) > 0 {
			v.pickCursor = (v.pickCursor - 1 + len(v.pickHosts)) % len(v.pickHosts)
		}
	case "enter":
		h := v.pickHosts[v.pickCursor]
		v.pickHostOpen = false
		var kc *db.KeychainItem
		if h.KeychainID != nil {
			kc, _ = v.store.GetKeychain(*h.KeychainID)
		}
		return v.ConnectHost(h, kc)
	}
	return nil
}

func (v *SFTPView) openInputModal(kind string, entry ssh.FileEntry) {
	if v.activePane == paneRemote && v.client == nil {
		return
	}
	v.inputModalKind = kind
	v.renameTarget = entry
	v.formErr = ""
	if kind == "rename" {
		v.inputField.SetValue(entry.Name)
		v.inputField.CursorEnd()
	} else {
		v.inputField.SetValue("")
	}
	v.inputField.Focus()
}

func (v *SFTPView) updateInputModal(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.inputModalKind = ""
		v.inputField.Blur()
		return nil
	case "enter":
		name := strings.TrimSpace(v.inputField.Value())
		if name == "" {
			v.formErr = "Name is required."
			return nil
		}
		if v.inputModalKind == "mkdir" {
			if v.activePane == paneLocal {
				if err := ssh.MkdirLocal(v.localDir, name); err != nil {
					v.formErr = err.Error()
					return nil
				}
				v.inputModalKind = ""
				v.refreshLocal()
				return toastCmd(components.ToastSuccess, "Created folder "+name)
			}
			if err := v.client.MkdirRemote(v.remoteDir, name); err != nil {
				v.formErr = err.Error()
				return nil
			}
			v.inputModalKind = ""
			return tea.Batch(v.refreshRemoteCmd(), toastCmd(components.ToastSuccess, "Created remote folder "+name))
		}
		// Rename
		if v.activePane == paneLocal {
			if err := ssh.RenameLocal(v.renameTarget.FullPath, name); err != nil {
				v.formErr = err.Error()
				return nil
			}
			v.inputModalKind = ""
			v.refreshLocal()
			return toastCmd(components.ToastSuccess, "Renamed to "+name)
		}
		if err := v.client.RenameRemote(v.renameTarget.FullPath, name); err != nil {
			v.formErr = err.Error()
			return nil
		}
		v.inputModalKind = ""
		return tea.Batch(v.refreshRemoteCmd(), toastCmd(components.ToastSuccess, "Renamed to "+name))
	}

	var cmd tea.Cmd
	v.inputField, cmd = v.inputField.Update(msg)
	return cmd
}

func (v *SFTPView) executeDelete() tea.Cmd {
	target := v.deleteTarget
	if v.deletePane == paneLocal {
		if err := ssh.RemoveLocal(target.FullPath); err != nil {
			return toastCmd(components.ToastError, fmt.Sprintf("Delete failed: %v", err))
		}
		v.refreshLocal()
		return toastCmd(components.ToastSuccess, "Deleted "+target.Name)
	}
	if v.client == nil {
		return nil
	}
	if err := v.client.RemoveRemote(target.FullPath); err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Remote delete failed: %v", err))
	}
	return tea.Batch(v.refreshRemoteCmd(), toastCmd(components.ToastSuccess, "Deleted "+target.Name))
}

func (v *SFTPView) View(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}

	if v.confirm.Active {
		return v.confirm.View(w, h)
	}
	if v.pickHostOpen {
		items := make([]string, len(v.pickHosts))
		for i, host := range v.pickHosts {
			osInfo := theme.LookupOS(host.OSType)
			items[i] = fmt.Sprintf("%s %s (%s@%s:%d)", osInfo.Icon, host.Name, host.Username, host.Hostname, host.Port)
		}
		body := RenderPickerList(items, v.pickCursor, clampInt(h-10, 4, 14), 50)
		footer := theme.InputHint.Render("[j/k] Select Host  [Enter] Connect SFTP  [Esc] Cancel")
		return components.RenderModal(w, h, "Connect SFTP Host", body, footer, 62)
	}
	if v.inputModalKind != "" {
		title := "New Folder"
		if v.inputModalKind == "rename" {
			title = "Rename " + v.renameTarget.Name
		}
		body := RenderRoundedInputBox(&v.inputField, minInt(46, maxInt(18, w-12)), true, theme.Mantle, theme.Base)
		if v.formErr != "" {
			body += "\n\n" + theme.StatusError.Render("✖ "+v.formErr)
		}
		footer := theme.InputHint.Render("[Enter] Confirm  [Esc] Cancel")
		return components.RenderModal(w, h, title, body, footer, 56)
	}

	// Row 0: SFTP Toolbar
	toolbar := v.renderToolbar(w)
	v.emptyConnectBtnRect = rect{}
	v.uploadBtnRect = rect{}
	v.downloadBtnRect = rect{}

	transferBarH := 0
	if v.transferring {
		transferBarH = 1
	}
	// Row 1 holds the clickable transfer buttons (and drag feedback). Hide it
	// on very short terminals so both panes still get their minimum height.
	transferRowH := 0
	if h >= 7 {
		transferRowH = 1
	}
	v.transferRowVisible = transferRowH == 1
	paneTop := 1 + transferRowH
	paneH := maxInt(5, h-1-transferRowH-transferBarH)
	leftW := w / 2
	rightW := w - leftW

	// While dragging, highlight the pane that will receive the drop.
	dropPane := -1
	if v.dragging {
		if v.dragPane == paneLocal {
			dropPane = paneRemote
		} else {
			dropPane = paneLocal
		}
	}

	leftPane := v.renderPane(
		leftW, paneH,
		"󰋊 Local: "+v.localDir,
		v.localEntries,
		v.localCursor, &v.localOffset,
		v.activePane == paneLocal,
		dropPane == paneLocal,
		"Empty directory.",
	)

	var rightPane string
	if v.connecting {
		border := theme.BorderInactive
		bg := theme.Mantle
		if v.activePane == paneRemote {
			border = theme.BorderActive
			bg = theme.Base
		}
		v.connSpinner.Style = lipgloss.NewStyle().Foreground(theme.Yellow).Background(bg)
		msg := v.connSpinner.View() + lipgloss.NewStyle().Foreground(theme.Text).Background(bg).Render(" Connecting SFTP...")
		inner := lipgloss.Place(
			maxInt(8, rightW-4), maxInt(3, paneH-2),
			lipgloss.Center, lipgloss.Center,
			msg,
			lipgloss.WithWhitespaceBackground(bg),
		)
		rightPane = border.Padding(0, 1).Width(rightW - 2).Height(paneH - 2).MaxWidth(rightW).MaxHeight(paneH).Render(inner)
	} else if v.client == nil || v.remoteHost == nil {
		border := theme.BorderInactive
		bg := theme.Mantle
		if v.activePane == paneRemote {
			border = theme.BorderActive
			bg = theme.Base
		}
		btn := theme.ButtonActive.Render("Select Host (c)")
		prompt := theme.InspectorLabel.Background(bg).Render("No remote SFTP host connected.") + "\n\n" + btn
		inner := lipgloss.Place(
			maxInt(8, rightW-4), maxInt(3, paneH-2),
			lipgloss.Center, lipgloss.Center,
			prompt,
			lipgloss.WithWhitespaceBackground(bg),
		)
		rightPane = border.Padding(0, 1).Width(rightW - 2).Height(paneH - 2).MaxWidth(rightW).MaxHeight(paneH).Render(inner)
		btnW := lipgloss.Width(btn)
		btnX0 := leftW + maxInt(2, (rightW-btnW)/2)
		btnY0 := paneTop + maxInt(1, (paneH/2))
		v.emptyConnectBtnRect = rect{x0: btnX0, y0: btnY0, x1: btnX0 + btnW, y1: btnY0 + 2}
	} else {
		rightTitle := fmt.Sprintf("󰒋 %s: %s", v.remoteHost.Name, v.remoteDir)
		rightPane = v.renderPane(
			rightW, paneH,
			rightTitle,
			v.remoteEntries,
			v.remoteCursor, &v.remoteOffset,
			v.activePane == paneRemote,
			dropPane == paneRemote,
			"Empty remote directory.",
		)
	}

	split := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
	out := toolbar
	if v.transferRowVisible {
		out += "\n" + v.renderTransferRow(w)
	}
	out += "\n" + split
	if v.transferring {
		out += "\n" + v.renderTransferBar(w)
	}
	return out
}

func (v *SFTPView) renderToolbar(w int) string {
	title := theme.SectionTitle.Background(theme.Crust).Render("󰉋 SFTP Dual-Pane")
	hiddenLabel := "Hidden: OFF"
	if v.showHidden {
		hiddenLabel = "Hidden: ON"
	}
	hiddenBtn := theme.SearchPill.Render(". " + hiddenLabel)
	refreshBtn := theme.SearchPill.Render("↻ Refresh")
	hostLabel := "Select Host (c)"
	if v.remoteHost != nil && v.client != nil {
		hostLabel = "Host: " + v.remoteHost.Name
	}
	hostBtn := theme.ActionPill.Render(hostLabel)

	right := hiddenBtn + theme.SpaceBg(theme.Crust, 1) + refreshBtn + theme.SpaceBg(theme.Crust, 1) + hostBtn
	gap := maxInt(1, w-lipgloss.Width(title)-lipgloss.Width(right)-1)
	line := theme.SpaceBg(theme.Crust, 1) + title + theme.SpaceBg(theme.Crust, gap) + right
	line = components.Truncate(line, w)
	if lw := lipgloss.Width(line); lw < w {
		line += theme.SpaceBg(theme.Crust, w-lw)
	}

	curX := 1 + lipgloss.Width(title) + gap
	hw := lipgloss.Width(hiddenBtn)
	rw := lipgloss.Width(refreshBtn)
	hostW := lipgloss.Width(hostBtn)

	v.hiddenBtnRect = rect{x0: curX, y0: 0, x1: curX + hw, y1: 1}
	v.refreshBtnRect = rect{x0: curX + hw + 1, y0: 0, x1: curX + hw + 1 + rw, y1: 1}
	v.selectHostBtnRect = rect{x0: curX + hw + 1 + rw + 1, y0: 0, x1: curX + hw + 1 + rw + 1 + hostW, y1: 1}

	return line
}

// renderTransferRow renders the clickable Upload/Download buttons and the
// in-app drag hint. Button hitboxes are row 1, relative to the SFTPView origin.
func (v *SFTPView) renderTransferRow(w int) string {
	v.uploadBtnRect = rect{}
	v.downloadBtnRect = rect{}

	if v.dragging {
		target := "remote"
		if v.dragPane == paneRemote {
			target = "local"
		}
		line := theme.SpaceBg(theme.Crust, 1) +
			theme.SectionTitle.Background(theme.Crust).Render("⇅ Drag") +
			theme.SpaceBg(theme.Crust, 1) +
			theme.InputHint.Background(theme.Crust).Render(
				components.Truncate(v.dragEntry.Name+"  →  release in "+target+" pane to transfer", maxInt(1, w-9)),
			)
		line = components.Truncate(line, w)
		if lw := lipgloss.Width(line); lw < w {
			line += theme.SpaceBg(theme.Crust, w-lw)
		}
		return line
	}

	label := theme.SectionTitle.Background(theme.Crust).Render("⇅ Transfer")
	upBtn := theme.ActionBarPrimaryPill.Render("↑ Upload")
	downBtn := theme.ActionBarPrimaryPill.Render("↓ Download")

	line := theme.SpaceBg(theme.Crust, 1) + label + theme.SpaceBg(theme.Crust, 2)
	x := 1 + lipgloss.Width(label) + 2

	v.uploadBtnRect = rect{x0: x, y0: 1, x1: x + lipgloss.Width(upBtn), y1: 2}
	line += upBtn
	line += theme.SpaceBg(theme.Crust, 1)
	x += lipgloss.Width(upBtn) + 1

	v.downloadBtnRect = rect{x0: x, y0: 1, x1: x + lipgloss.Width(downBtn), y1: 2}
	line += downBtn
	x += lipgloss.Width(downBtn)

	if entry, ok := v.selectedEntry(); ok && !entry.IsParent {
		if room := w - x; room > 4 {
			line += theme.InputHint.Background(theme.Crust).Render(
				components.Truncate("  selected: "+entry.Name, room),
			)
		}
	}

	line = components.Truncate(line, w)
	if lw := lipgloss.Width(line); lw < w {
		line += theme.SpaceBg(theme.Crust, w-lw)
	}
	return line
}

func (v *SFTPView) renderPane(
	outerW, outerH int,
	title string,
	entries []ssh.FileEntry,
	cursor int,
	offset *int,
	focused bool,
	dropTarget bool,
	emptyMsg string,
) string {
	innerW := maxInt(8, outerW-4)
	innerH := maxInt(3, outerH-2)

	hdr := padBgLine(theme.InspectorTitle.Background(theme.Base).Render(components.Truncate(title, innerW)), innerW, theme.Base)
	cols := []Column{
		{Title: "Name", Weight: 4, MinWidth: 10},
		{Title: "Size", Weight: 1, MinWidth: 7, MaxWidth: 10},
		{Title: "Modified", Weight: 2, MinWidth: 11, MaxWidth: 14},
	}

	rows := make([][]string, len(entries))
	isDirFlags := make([]bool, len(entries))
	for i, e := range entries {
		isDirFlags[i] = e.IsDir
		name := e.Name
		sz := ssh.HumanBytes(e.Size)
		if e.IsDir {
			if e.IsParent {
				name = "󰁝 .."
			} else {
				name = "󰉋 " + e.Name
			}
			sz = "<DIR>"
		} else {
			name = "󰈔 " + e.Name
		}
		mod := "-"
		if !e.ModTime.IsZero() {
			mod = e.ModTime.Local().Format("01-02 15:04")
		}
		rows[i] = []string{name, sz, mod}
	}

	styler := func(rowIdx, colIdx int, paddedCell string) string {
		if colIdx == 0 && rowIdx < len(isDirFlags) && isDirFlags[rowIdx] {
			return lipgloss.NewStyle().Foreground(theme.Blue).Background(theme.Base).Bold(true).Render(paddedCell)
		}
		return theme.RowNormal.Render(paddedCell)
	}

	tbl, newOff := RenderTableStyled(innerW, maxInt(1, innerH-1), cols, rows, cursor, *offset, emptyMsg, styler)
	*offset = newOff

	border := theme.BorderInactive.Background(theme.Base)
	if focused {
		border = theme.BorderActive
	}
	if dropTarget {
		border = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(theme.Green).
			BorderBackground(theme.Crust).
			Background(theme.Base)
	}
	return border.
		Padding(0, 1).
		Width(outerW - 2).
		Height(outerH - 2).
		MaxWidth(outerW).
		MaxHeight(outerH).
		Render(hdr + "\n" + tbl)
}

func (v *SFTPView) renderTransferBar(w int) string {
	p := v.transferProg
	pct := 0
	if p.TotalBytes > 0 {
		pct = int((p.BytesDone * 100) / p.TotalBytes)
		if pct > 100 {
			pct = 100
		}
	}
	barW := 16
	filled := (pct * barW) / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barW-filled)
	speed := ssh.HumanBytes(int64(p.BytesPerSec)) + "/s"
	txt := fmt.Sprintf(" %s %s  [%s] %d%%  (%s)  [Esc] Cancel", v.transferAction, p.Filename, bar, pct, speed)
	return theme.ActionPill.Width(w).MaxHeight(1).Render(components.Truncate(txt, w))
}

func (v *SFTPView) Hints() []components.KeyHint {
	if v.transferring {
		return []components.KeyHint{
			{Key: "Esc", Desc: "Cancel Transfer"},
		}
	}
	return []components.KeyHint{
		{Key: "Tab", Desc: "Switch Pane"},
		{Key: "Enter", Desc: "Open"},
		{Key: "u/d", Desc: "Upload/Download"},
		{Key: "F5", Desc: "Transfer"},
		{Key: "Drag", Desc: "Send to pane"},
		{Key: "m", Desc: "Mkdir"},
		{Key: "r", Desc: "Rename"},
		{Key: "x", Desc: "Delete"},
		{Key: ".", Desc: "Hidden"},
		{Key: "c", Desc: "Connect"},
		{Key: "Alt+1", Desc: "Vaults"},
	}
}
