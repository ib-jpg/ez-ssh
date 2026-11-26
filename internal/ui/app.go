package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/config"
	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ssh"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
	"github.com/ib-jpg/ez-ssh/internal/ui/views"
)

const (
	TabHosts = iota
	TabKeychain
	TabPortFwd
	TabSnippets
	TabKnownHosts
	TabLogs
)

const (
	wsVaults = iota
	wsSFTP
	wsSession
)

const (
	topBarHeight  = 1
	topGapRows    = 1 // breathing room between top bar and workspace
	bottomGapRows = 1 // breathing room between workspace and footer
	minTermWidth  = 48
	minTermHeight = 14
)

type topHitKind int

const (
	hitVaults topHitKind = iota
	hitSFTP
	hitSession
	hitSessionClose
	hitNewSession
)

type topHit struct {
	kind       topHitKind
	sessionIdx int
	x0, x1     int
}

type copyIDFinishedMsg struct {
	host    db.Host
	askpass *ssh.AskpassServer
	err     error
}

type introDoneMsg struct{}

type sidebarItem struct {
	icon  string
	label string
}

var sidebarItems = []sidebarItem{
	{icon: "󰒋", label: "Hosts"},
	{icon: "󰌆", label: "Keychain"},
	{icon: "󰛳", label: "Port Fwd"},
	{icon: "󰆍", label: "Snippets"},
	{icon: "󰢬", label: "Known Host"},
	{icon: "󱂅", label: "Logs"},
}

// AppModel is the root Bubble Tea model for ez-ssh v1.0, orchestrating
// the Top Workspace Tab Bar, Vaults modules, SFTP, and live embedded SSH tabs.
type AppModel struct {
	cfg       *config.Config
	store     *db.Store
	vault     *crypto.Vault
	tunnelMgr *ssh.TunnelManager

	ctx    context.Context
	cancel context.CancelFunc

	width         int
	height        int
	activeWS      int
	activeTab     int
	showInspector bool
	counts        db.Counts
	toast         components.ToastModel

	lastTunnelState map[int64]ssh.TunnelState

	winter        components.WinterWidget
	winterTickGen int64
	winterRunning bool

	hostsView      *views.HostsView
	keychainView   *views.KeychainView
	portFwdView    *views.PortFwdView
	snippetsView   *views.SnippetsView
	knownHostsView *views.KnownHostsView
	logsView       *views.LogsView

	sftpView         *views.SFTPView
	sessions         []*views.SessionTab
	activeSessionIdx int
	nextSessionID    int64
	topTabHits       []topHit

	quickConnectOpen  bool
	quickConnectHosts []db.Host
	quickConnectIdx   int

	intro        bool
	introVersion string
	version      string

	lastClickTime time.Time
	lastClickX    int
	lastClickY    int
}

// NewAppModel creates the root TUI model initialized with all views and DB counts.
func NewAppModel(cfg *config.Config, store *db.Store, vault *crypto.Vault, tunnelMgr *ssh.TunnelManager) *AppModel {
	ctx, cancel := context.WithCancel(context.Background())

	m := &AppModel{
		cfg:             cfg,
		store:           store,
		vault:           vault,
		tunnelMgr:       tunnelMgr,
		ctx:             ctx,
		cancel:          cancel,
		width:           100,
		height:          30,
		activeWS:        wsVaults,
		activeTab:       TabHosts,
		showInspector:   true,
		version:         "1.0.0",
		lastTunnelState: make(map[int64]ssh.TunnelState),
		winter:          components.NewWinterWidget(),
		hostsView:       views.NewHostsView(store, vault, cfg.SSHConfigPath),
		keychainView:    views.NewKeychainView(store, vault, cfg.SSHDir),
		portFwdView:     views.NewPortFwdView(store, tunnelMgr),
		snippetsView:    views.NewSnippetsView(store, vault, cfg.KnownHostsPath),
		knownHostsView:  views.NewKnownHostsView(cfg.KnownHostsPath),
		logsView:        views.NewLogsView(store),
		sftpView:        views.NewSFTPView(store, vault, cfg.KnownHostsPath),
	}
	m.refreshCounts()
	m.syncViewSizes()
	return m
}

// SetVersion updates the version string displayed in the UI sidebar and splash.
func (m *AppModel) SetVersion(v string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v != "" {
		m.version = v
	}
}

// EnableIntro schedules the branded ASCII banner on the next startup. Tests
// construct the model directly and therefore skip the splash by default.
func (m *AppModel) EnableIntro(version string) {
	m.SetVersion(version)
	m.intro = true
	m.introVersion = version
}

// Init initializes sub-views, starts tunnel event listener, and begins gentle snow ticker.
func (m *AppModel) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.activeView().Init(),
		listenTunnelEvents(m.ctx, m.tunnelMgr.Events()),
		m.ensureWinterTicker(),
	}
	if m.intro {
		cmds = append(cmds, tea.Tick(1400*time.Millisecond, func(time.Time) tea.Msg {
			return introDoneMsg{}
		}))
	}
	return tea.Batch(cmds...)
}

func (m *AppModel) showWinterWidget() bool {
	lm := m.computeVaultLayout()
	return m.height >= 26 && lm.bodyH >= 24
}

func (m *AppModel) ensureWinterTicker() tea.Cmd {
	if m.activeWS != wsVaults || !m.showWinterWidget() {
		m.winterRunning = false
		return nil
	}
	if m.winterRunning {
		return nil
	}
	m.winterTickGen++
	gen := m.winterTickGen
	m.winterRunning = true
	return tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
		return components.WinterTickMsg{Gen: gen}
	})
}

func listenTunnelEvents(ctx context.Context, ch <-chan ssh.TunnelEvent) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			return views.TunnelEventMsg{Event: ev}
		}
	}
}

func (m *AppModel) allViews() []views.View {
	return []views.View{
		m.hostsView,
		m.keychainView,
		m.portFwdView,
		m.snippetsView,
		m.knownHostsView,
		m.logsView,
	}
}

func (m *AppModel) activeView() views.View {
	vs := m.allViews()
	if m.activeTab < 0 || m.activeTab >= len(vs) {
		return vs[0]
	}
	return vs[m.activeTab]
}

func (m *AppModel) activeSession() *views.SessionTab {
	if m.activeWS != wsSession || m.activeSessionIdx < 0 || m.activeSessionIdx >= len(m.sessions) {
		return nil
	}
	return m.sessions[m.activeSessionIdx]
}

func (m *AppModel) refreshCounts() {
	if c, err := m.store.Counts(); err == nil {
		m.counts = c
	}
}

func (m *AppModel) reloadAllViews() {
	for _, v := range m.allViews() {
		_ = v.Reload()
	}
	m.refreshCounts()
	m.syncActiveHostDots()
}

func (m *AppModel) syncActiveHostDots() {
	activeIDs := make(map[int64]bool)
	for _, s := range m.sessions {
		if s.State == views.StateConnected {
			activeIDs[s.Host.ID] = true
		}
	}
	m.hostsView.SetActiveHostIDs(activeIDs)
}

type layoutMetrics struct {
	sidebarW        int
	rightW          int
	bodyH           int
	mainOuterH      int
	inspectorOuterH int
	mainInnerW      int
	mainInnerH      int
	inspInnerW      int
	inspInnerH      int
}

func (m *AppModel) computeVaultLayout() layoutMetrics {
	w := m.width
	h := m.height
	if w < minTermWidth {
		w = minTermWidth
	}
	if h < minTermHeight {
		h = minTermHeight
	}

	// Keep the sidebar wide enough for the fixed 18-cell winter scene even on
	// narrow terminals (24 outer cells => 22 inner cells after the divider).
	sidebarW := 24
	rightW := w - sidebarW
	// top bar + gap above workspace + gap below workspace + footer hints
	bodyH := h - topBarHeight - topGapRows - bottomGapRows - 1
	if bodyH < 6 {
		bodyH = 6
	}

	useBottomInspector := m.showInspector && m.activeTab != TabHosts && bodyH >= 14
	inspectorOuterH := 0
	if useBottomInspector {
		inspectorOuterH = bodyH / 3
		if inspectorOuterH < 6 {
			inspectorOuterH = 6
		}
		if inspectorOuterH > 8 {
			inspectorOuterH = 8
		}
	}
	mainOuterH := bodyH - inspectorOuterH

	if m.activeTab == TabHosts {
		// Hosts renders edge-to-edge on Layer 0 Crust without an outer box border.
		mainOuterH = bodyH
	}
	mainInnerW := rightW
	mainInnerH := mainOuterH
	if mainInnerW < 10 {
		mainInnerW = 10
	}
	if mainInnerH < 4 {
		mainInnerH = 4
	}

	inspInnerW := maxInt(10, rightW-4)
	inspInnerH := 0
	if inspectorOuterH > 2 {
		inspInnerH = inspectorOuterH - 2
	}

	return layoutMetrics{
		sidebarW:        sidebarW,
		rightW:          rightW,
		bodyH:           bodyH,
		mainOuterH:      mainOuterH,
		inspectorOuterH: inspectorOuterH,
		mainInnerW:      mainInnerW,
		mainInnerH:      mainInnerH,
		inspInnerW:      inspInnerW,
		inspInnerH:      inspInnerH,
	}
}

func (m *AppModel) sessionViewportSize() (int, int) {
	w := m.width
	h := m.height - topBarHeight - topGapRows - bottomGapRows
	if w < 10 {
		w = 80
	}
	if h < 4 {
		h = 22
	}
	return w, h
}

func (m *AppModel) syncViewSizes() {
	lm := m.computeVaultLayout()
	for _, v := range m.allViews() {
		v.SetSize(lm.mainInnerW, lm.mainInnerH)
	}
	m.hostsView.SetShowInspector(m.showInspector)

	sftpH := maxInt(6, m.height-topBarHeight-topGapRows-bottomGapRows-1)
	m.sftpView.SetSize(maxInt(40, m.width), sftpH)

	sw, sh := m.sessionViewportSize()
	for _, s := range m.sessions {
		s.SetSize(sw, sh)
	}
}

// Update handles all Bubble Tea messages across Top Tab Bar, Vaults, SFTP, and Sessions.
func (m *AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case introDoneMsg:
		m.intro = false
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncViewSizes()
		return m, m.ensureWinterTicker()

	case components.WinterTickMsg:
		if msg.Gen != m.winterTickGen {
			return m, nil
		}
		if m.activeWS != wsVaults || !m.showWinterWidget() {
			m.winterRunning = false
			return m, nil
		}
		m.winter.Step()
		gen := m.winterTickGen
		return m, tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
			return components.WinterTickMsg{Gen: gen}
		})

	case views.ToggleInspectorMsg:
		m.showInspector = !m.showInspector
		m.syncViewSizes()
		return m, nil

	case components.ToastExpiredMsg:
		m.toast.Update(msg)
		return m, nil

	case views.ToastMsg:
		return m, m.toast.Show(msg.Level, msg.Text)

	case views.DataChangedMsg:
		m.reloadAllViews()
		return m, nil

	case views.TunnelEventMsg:
		var cmds []tea.Cmd
		cmds = append(cmds, listenTunnelEvents(m.ctx, m.tunnelMgr.Events()))
		st := msg.Event.Status
		prev := m.lastTunnelState[st.ID]
		m.lastTunnelState[st.ID] = st.State
		if prev != st.State {
			switch st.State {
			case ssh.TunnelActive:
				cmds = append(cmds, m.toast.Show(components.ToastSuccess, fmt.Sprintf("Tunnel #%d active", st.ID)))
			case ssh.TunnelError:
				cmds = append(cmds, m.toast.Show(components.ToastError, st.Error))
			}
		}
		return m, tea.Batch(cmds...)

	case views.OpenSessionMsg:
		return m, m.openSessionTab(msg.Host, msg.Keychain)

	case views.OpenSFTPMsg:
		m.activeWS = wsSFTP
		m.winterRunning = false
		cmd := m.sftpView.ConnectHost(msg.Host, msg.Keychain)
		return m, cmd

	case views.CloseSessionTabMsg:
		m.closeSessionByID(msg.TabID)
		return m, m.ensureWinterTicker()

	case views.EditHostFromSessionMsg:
		m.activeWS = wsVaults
		m.switchTab(TabHosts)
		m.hostsView.OpenEditForHost(msg.HostID)
		return m, m.ensureWinterTicker()

	case views.SessionConnectResultMsg:
		var cmd tea.Cmd
		for _, s := range m.sessions {
			if s.ID == msg.TabID {
				cmd = s.Update(msg)
				break
			}
		}
		m.syncActiveHostDots()
		return m, cmd

	case views.SessionDirtyMsg:
		for _, s := range m.sessions {
			if s.ID == msg.TabID {
				return m, s.Update(msg)
			}
		}
		return m, nil

	case views.SessionExitedMsg:
		var cmd tea.Cmd
		for _, s := range m.sessions {
			if s.ID == msg.TabID {
				cmd = s.Update(msg)
				break
			}
		}
		m.syncActiveHostDots()
		return m, cmd

	case spinner.TickMsg:
		var cmds []tea.Cmd
		for _, s := range m.sessions {
			if s.IsSpinnerActive() {
				if c := s.Update(msg); c != nil {
					cmds = append(cmds, c)
				}
			}
		}
		if m.sftpView.IsSpinnerActive() {
			if c := m.sftpView.Update(msg); c != nil {
				cmds = append(cmds, c)
			}
		}
		if m.activeWS == wsVaults && (m.activeTab == TabKeychain || m.activeTab == TabSnippets) {
			if c := m.activeView().Update(msg); c != nil {
				cmds = append(cmds, c)
			}
		}
		return m, tea.Batch(cmds...)

	case views.KeyGenResultMsg:
		cmd := m.keychainView.Update(msg)
		return m, cmd

	case views.SnippetExecResultMsg:
		cmd := m.snippetsView.Update(msg)
		return m, cmd

	case views.ExecCopyIDMsg:
		cmd, askpass, err := ssh.BuildCopyIDCmd(
			msg.PubKeyPath, msg.Host, msg.HostKeychain, m.vault, m.cfg.RunDir,
		)
		if err != nil {
			return m, m.toast.Show(components.ToastError, fmt.Sprintf("ssh-copy-id: %v", err))
		}
		host := msg.Host
		return m, tea.ExecProcess(cmd, func(execErr error) tea.Msg {
			return copyIDFinishedMsg{
				host:    host,
				askpass: askpass,
				err:     execErr,
			}
		})

	case copyIDFinishedMsg:
		if msg.askpass != nil {
			_ = msg.askpass.Close()
		}
		if msg.err != nil {
			return m, m.toast.Show(components.ToastError, fmt.Sprintf("ssh-copy-id failed: %v", msg.err))
		}
		return m, m.toast.Show(components.ToastSuccess, fmt.Sprintf("Public key installed on %s", msg.host.Name))

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Route background async messages (SFTP list/transfer, etc.)
	if cmd := m.sftpView.Update(msg); cmd != nil {
		return m, cmd
	}
	if m.activeWS == wsVaults {
		return m, m.activeView().Update(msg)
	}
	return m, nil
}

func (m *AppModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.intro {
		m.intro = false // any key skips the intro without triggering an action
		return m, nil
	}
	// Global workspace tab shortcuts work across ALL workspaces (even inside SSH PTY!).
	switch msg.String() {
	case "alt+1":
		m.quickConnectOpen = false
		m.activeWS = wsVaults
		return m, m.ensureWinterTicker()
	case "alt+2":
		m.quickConnectOpen = false
		m.activeWS = wsSFTP
		m.winterRunning = false
		return m, nil
	case "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
		idx := int(msg.String()[len(msg.String())-1] - '3')
		if idx >= 0 && idx < len(m.sessions) {
			m.quickConnectOpen = false
			m.activeWS = wsSession
			m.activeSessionIdx = idx
			m.winterRunning = false
		}
		return m, nil
	case "alt+left":
		m.quickConnectOpen = false
		m.cycleWorkspace(-1)
		return m, m.ensureWinterTicker()
	case "alt+right":
		m.quickConnectOpen = false
		m.cycleWorkspace(1)
		return m, m.ensureWinterTicker()
	case "ctrl+w":
		if m.activeWS == wsSession && len(m.sessions) > 0 {
			m.closeSessionAt(m.activeSessionIdx)
			return m, m.ensureWinterTicker()
		}
	}

	if m.quickConnectOpen {
		return m, m.updateQuickConnect(msg)
	}

	// Connected SSH session receives ALL remaining keys directly (including Ctrl+C, Tab, Esc, q).
	if sess := m.activeSession(); sess != nil {
		if msg.String() == "ctrl+c" && sess.State != views.StateConnected {
			m.Shutdown()
			return m, tea.Quit
		}
		return m, sess.Update(msg)
	}

	if m.activeWS == wsSFTP {
		if msg.String() == "ctrl+c" {
			m.Shutdown()
			return m, tea.Quit
		}
		if msg.String() == "q" && !m.sftpView.IsModalOpen() {
			m.Shutdown()
			return m, tea.Quit
		}
		return m, m.sftpView.Update(msg)
	}

	// wsVaults
	if msg.String() == "ctrl+c" {
		m.Shutdown()
		return m, tea.Quit
	}

	av := m.activeView()
	if av.IsModalOpen() || av.IsSearching() {
		return m, av.Update(msg)
	}

	switch msg.String() {
	case "q":
		m.Shutdown()
		return m, tea.Quit
	case "tab":
		m.switchTab((m.activeTab + 1) % len(sidebarItems))
		return m, nil
	case "shift+tab":
		m.switchTab((m.activeTab + len(sidebarItems) - 1) % len(sidebarItems))
		return m, nil
	case "1":
		m.switchTab(TabHosts)
		return m, nil
	case "2":
		m.switchTab(TabKeychain)
		return m, nil
	case "3":
		m.switchTab(TabPortFwd)
		return m, nil
	case "4":
		m.switchTab(TabSnippets)
		return m, nil
	case "5":
		m.switchTab(TabKnownHosts)
		return m, nil
	case "6":
		m.switchTab(TabLogs)
		return m, nil
	case "i":
		m.showInspector = !m.showInspector
		m.syncViewSizes()
		return m, nil
	}

	return m, av.Update(msg)
}

func (m *AppModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.intro {
		m.intro = false // a click skips the intro without reaching any view
		return m, nil
	}
	if m.quickConnectOpen {
		return m, nil
	}

	// Row 0 is the global Top Workspace Tab Bar.
	if msg.Y == 0 && msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		for _, h := range m.topTabHits {
			if msg.X >= h.x0 && msg.X < h.x1 {
				switch h.kind {
				case hitVaults:
					m.activeWS = wsVaults
					return m, m.ensureWinterTicker()
				case hitSFTP:
					m.activeWS = wsSFTP
					m.winterRunning = false
				case hitSession:
					m.activeWS = wsSession
					m.activeSessionIdx = h.sessionIdx
					m.winterRunning = false
				case hitSessionClose:
					m.closeSessionAt(h.sessionIdx)
					return m, m.ensureWinterTicker()
				case hitNewSession:
					return m, m.openQuickConnect()
				}
				return m, nil
			}
		}
		return m, nil
	}

	// Adjust Y coordinate for views below the Top Workspace Tab Bar and its
	// breathing-room row.
	adjMsg := msg
	adjMsg.Y = msg.Y - topBarHeight - topGapRows
	if adjMsg.Y < 0 {
		adjMsg.Y = 0
	}

	if sess := m.activeSession(); sess != nil {
		return m, sess.Update(adjMsg)
	}

	if m.activeWS == wsSFTP {
		return m, m.sftpView.Update(adjMsg)
	}

	if m.width < minTermWidth || m.height < minTermHeight {
		return m, nil
	}
	lm := m.computeVaultLayout()

	// Special mouse routing for HostsView (supports right-click & floating dropdown overlay)
	if m.activeTab == TabHosts {
		if m.hostsView.IsDropdownOpen() && msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			return m, m.hostsView.HandleMouse(msg.X-lm.sidebarW, adjMsg.Y, msg.Button, false)
		}
		if m.hostsView.IsModalOpen() {
			return m, m.hostsView.Update(adjMsg)
		}
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			return m, m.hostsView.Update(adjMsg)
		}
		if msg.Action == tea.MouseActionPress && (msg.Button == tea.MouseButtonLeft || msg.Button == tea.MouseButtonRight) {
			if msg.Button == tea.MouseButtonLeft && msg.X >= 0 && msg.X < lm.sidebarW-1 && adjMsg.Y >= 0 && adjMsg.Y < lm.bodyH {
				tabIdx := adjMsg.Y - 3
				if tabIdx >= 0 && tabIdx < len(sidebarItems) {
					m.switchTab(tabIdx)
				}
				return m, nil
			}
			if msg.X >= lm.sidebarW && adjMsg.Y >= 0 && adjMsg.Y < lm.bodyH {
				doubleClick := false
				if msg.Button == tea.MouseButtonLeft {
					now := time.Now()
					doubleClick = now.Sub(m.lastClickTime) < 400*time.Millisecond &&
						absInt(m.lastClickY-msg.Y) <= 1 && absInt(m.lastClickX-msg.X) <= 10
					m.lastClickTime = now
					m.lastClickX = msg.X
					m.lastClickY = msg.Y
				}
				return m, m.hostsView.HandleMouse(msg.X-lm.sidebarW, adjMsg.Y, msg.Button, doubleClick)
			}
		}
		return m, nil
	}

	// wsVaults mouse routing for Keychain / PortFwd / Snippets / KnownHosts / Logs
	av := m.activeView()
	if av.IsModalOpen() {
		return m, av.Update(adjMsg)
	}
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		return m, av.Update(adjMsg)
	}
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return m, nil
	}

	// Click in Vault left module sidebar
	if msg.X >= 0 && msg.X < lm.sidebarW-1 && adjMsg.Y >= 0 && adjMsg.Y < lm.bodyH {
		tabIdx := adjMsg.Y - 3
		if tabIdx >= 0 && tabIdx < len(sidebarItems) {
			m.switchTab(tabIdx)
		}
		return m, nil
	}

	// Click inside Vault main workspace pane (header bar + rounded table box)
	if msg.X >= lm.sidebarW && msg.X < lm.sidebarW+lm.rightW && adjMsg.Y >= 0 && adjMsg.Y < lm.mainOuterH {
		now := time.Now()
		doubleClick := now.Sub(m.lastClickTime) < 400*time.Millisecond &&
			absInt(m.lastClickY-msg.Y) <= 1 && absInt(m.lastClickX-msg.X) <= 10
		m.lastClickTime = now
		m.lastClickX = msg.X
		m.lastClickY = msg.Y

		return m, av.HandleClick(msg.X-lm.sidebarW, adjMsg.Y, doubleClick)
	}

	return m, nil
}

func (m *AppModel) openSessionTab(host db.Host, kc *db.KeychainItem) tea.Cmd {
	m.nextSessionID++
	sw, sh := m.sessionViewportSize()
	tab := views.NewSessionTab(
		m.nextSessionID, host, kc,
		m.store, m.vault, m.cfg.KnownHostsPath, m.cfg.LogsDir,
		sw, sh,
	)
	m.sessions = append(m.sessions, tab)
	m.activeSessionIdx = len(m.sessions) - 1
	m.activeWS = wsSession
	m.winterRunning = false
	m.syncActiveHostDots()
	return tab.Start()
}

func (m *AppModel) closeSessionByID(id int64) {
	for i, s := range m.sessions {
		if s.ID == id {
			m.closeSessionAt(i)
			return
		}
	}
}

func (m *AppModel) closeSessionAt(idx int) {
	if idx < 0 || idx >= len(m.sessions) {
		return
	}
	m.sessions[idx].Close()
	m.sessions = append(m.sessions[:idx], m.sessions[idx+1:]...)
	if len(m.sessions) == 0 {
		m.activeSessionIdx = 0
		if m.activeWS == wsSession {
			m.activeWS = wsVaults
		}
	} else if m.activeSessionIdx >= len(m.sessions) {
		m.activeSessionIdx = len(m.sessions) - 1
	}
	m.syncActiveHostDots()
}

func (m *AppModel) cycleWorkspace(delta int) {
	total := 2 + len(m.sessions)
	cur := 0
	switch m.activeWS {
	case wsVaults:
		cur = 0
	case wsSFTP:
		cur = 1
	case wsSession:
		cur = 2 + m.activeSessionIdx
	}
	next := (cur + delta + total) % total
	switch next {
	case 0:
		m.activeWS = wsVaults
	case 1:
		m.activeWS = wsSFTP
	default:
		m.activeWS = wsSession
		m.activeSessionIdx = next - 2
	}
}

func (m *AppModel) openQuickConnect() tea.Cmd {
	hosts, _ := m.store.ListHosts()
	if len(hosts) == 0 {
		m.activeWS = wsVaults
		m.switchTab(TabHosts)
		return m.hostsView.OpenNew()
	}
	m.quickConnectHosts = hosts
	m.quickConnectIdx = 0
	m.quickConnectOpen = true
	return nil
}

func (m *AppModel) updateQuickConnect(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		m.quickConnectOpen = false
		return nil
	case "j", "down":
		if len(m.quickConnectHosts) > 0 {
			m.quickConnectIdx = (m.quickConnectIdx + 1) % len(m.quickConnectHosts)
		}
	case "k", "up":
		if len(m.quickConnectHosts) > 0 {
			m.quickConnectIdx = (m.quickConnectIdx - 1 + len(m.quickConnectHosts)) % len(m.quickConnectHosts)
		}
	case "enter":
		if m.quickConnectIdx >= 0 && m.quickConnectIdx < len(m.quickConnectHosts) {
			h := m.quickConnectHosts[m.quickConnectIdx]
			m.quickConnectOpen = false
			var kc *db.KeychainItem
			if h.KeychainID != nil {
				kc, _ = m.store.GetKeychain(*h.KeychainID)
			}
			return m.openSessionTab(h, kc)
		}
	}
	return nil
}

func (m *AppModel) switchTab(idx int) {
	if idx < 0 || idx >= len(sidebarItems) {
		return
	}
	m.activeTab = idx
	_ = m.activeView().Reload()
	m.refreshCounts()
	m.syncViewSizes()
}

// Shutdown gracefully terminates all active SSH session tabs, SFTP, and port-forwarding tunnels.
func (m *AppModel) Shutdown() {
	m.cancel()
	for _, s := range m.sessions {
		s.Close()
	}
	m.sessions = nil
	if m.sftpView != nil {
		m.sftpView.Close()
	}
	m.tunnelMgr.ShutdownAll()
}

// View renders the Top Workspace Tab Bar + active workspace (Vaults / SFTP / Session) strictly within width x height.
func (m *AppModel) View() string {
	w := m.width
	h := m.height
	if w <= 0 || h <= 0 {
		return ""
	}

	if m.intro {
		banner := RenderBanner(m.introVersion, minInt(w, 80))
		splash := lipgloss.Place(
			w, h, lipgloss.Center, lipgloss.Center, banner,
			lipgloss.WithWhitespaceBackground(theme.Crust),
		)
		return clampFrame(splash, w, h)
	}

	if w < minTermWidth || h < minTermHeight {
		msg := theme.BrandStyle.Background(theme.Crust).Render(" EZ-SSH ") + "\n\n" +
			theme.InputHint.Background(theme.Crust).Render(fmt.Sprintf("Terminal too small (%dx%d)", w, h)) + "\n" +
			theme.VersionStyle.Background(theme.Crust).Render(fmt.Sprintf("Minimum %dx%d", minTermWidth, minTermHeight))
		return lipgloss.Place(
			w, h, lipgloss.Center, lipgloss.Center, msg,
			lipgloss.WithWhitespaceBackground(theme.Crust),
		)
	}

	topBar := m.renderTopTabBar(w)
	contentH := maxInt(1, h-topBarHeight)

	if m.quickConnectOpen {
		items := make([]string, len(m.quickConnectHosts))
		for i, hst := range m.quickConnectHosts {
			osInfo := theme.LookupOS(hst.OSType)
			items[i] = fmt.Sprintf("%s %s (%s@%s:%d)", osInfo.Icon, hst.Name, hst.Username, hst.Hostname, hst.Port)
		}
		body := views.RenderPickerList(items, m.quickConnectIdx, clampInt(contentH-10, 4, 14), 52)
		footer := theme.InputHint.Render("[j/k] Select Host  [Enter] Open Terminal Tab  [Esc] Cancel")
		modal := components.RenderModal(w, contentH, "New SSH Session Tab", body, footer, 62)
		return clampFrame(topBar+"\n"+modal, w, h)
	}

	var workspace string
	trailingGap := false
	gapLine := theme.SpaceBg(theme.Crust, w)

	switch m.activeWS {
	case wsSession:
		if sess := m.activeSession(); sess != nil {
			sessionH := maxInt(1, h-topBarHeight-topGapRows-bottomGapRows)
			workspace = sess.View(w, sessionH)
			trailingGap = true
		} else {
			m.activeWS = wsVaults
		}
	}

	if m.activeWS != wsSession {
		bodyH := maxInt(1, h-topBarHeight-topGapRows-bottomGapRows-1)
		switch m.activeWS {
		case wsSFTP:
			sftpBody := m.sftpView.View(w, bodyH)
			footer := components.RenderKeyHints(w, m.sftpView.Hints())
			workspace = sftpBody + "\n" + gapLine + "\n" + footer
		default:
			if modal := m.activeView().ViewModal(w, h-topBarHeight); modal != "" {
				return clampFrame(topBar+"\n"+modal, w, h)
			}
			workspace = m.renderVaults(w, bodyH)
			footer := components.RenderKeyHints(w, m.activeView().Hints())
			workspace += "\n" + gapLine + "\n" + footer
		}
	}

	full := topBar + "\n" + gapLine + "\n" + workspace
	if trailingGap {
		full += "\n" + gapLine
	}
	if m.toast.Visible() {
		full = components.OverlayTopRight(full, m.toast.View(w/2), w)
	}
	return clampFrame(full, w, h)
}

func (m *AppModel) renderTopTabBar(w int) string {
	m.topTabHits = m.topTabHits[:0]
	var b strings.Builder
	curX := 0

	appendSeg := func(rendered string, kind topHitKind, sessIdx int) bool {
		sw := lipgloss.Width(rendered)
		if curX+sw > w {
			return false
		}
		b.WriteString(rendered)
		m.topTabHits = append(m.topTabHits, topHit{kind: kind, sessionIdx: sessIdx, x0: curX, x1: curX + sw})
		curX += sw
		return true
	}

	vaultLabel := "󰒋 Vaults"
	if m.activeWS == wsVaults {
		appendSeg(theme.TabActive.Render(vaultLabel), hitVaults, -1)
	} else {
		appendSeg(theme.TabInactive.Render(vaultLabel), hitVaults, -1)
	}

	if curX < w {
		b.WriteString(theme.SpaceBg(theme.Mantle, 1))
		curX++
	}

	sftpLabel := "󰉋 SFTP"
	if m.activeWS == wsSFTP {
		appendSeg(theme.TabActive.Render(sftpLabel), hitSFTP, -1)
	} else {
		appendSeg(theme.TabInactive.Render(sftpLabel), hitSFTP, -1)
	}

	plusBtn := theme.TabPlus.Render("+")
	plusW := lipgloss.Width(plusBtn) + 1

	for i, s := range m.sessions {
		if curX+plusW+10 >= w {
			break
		}
		b.WriteString(theme.SpaceBg(theme.Mantle, 1))
		curX++

		isActive := m.activeWS == wsSession && m.activeSessionIdx == i
		tabBg := theme.Mantle
		nameFg := theme.Subtext0
		if isActive {
			tabBg = theme.Surface0
			nameFg = theme.Text
		}
		dot := sessionTabDot(s.State, tabBg)
		maxLabel := clampInt(w-curX-plusW-7, 6, 18)
		hostName := lipgloss.NewStyle().Background(tabBg).Foreground(nameFg).Bold(isActive).
			Render(components.Truncate(s.Host.Name, maxLabel))
		namePart := dot + theme.SpaceBg(tabBg, 1) + hostName

		var mainSeg, closeSeg string
		if isActive {
			mainSeg = lipgloss.NewStyle().Background(theme.Surface0).Foreground(theme.Text).Bold(true).Padding(0, 1).Render(namePart)
			closeSeg = lipgloss.NewStyle().Background(theme.Surface0).Foreground(theme.Subtext0).PaddingRight(1).Render("×")
		} else {
			mainSeg = lipgloss.NewStyle().Background(theme.Mantle).Foreground(theme.Subtext0).Padding(0, 1).Render(namePart)
			closeSeg = lipgloss.NewStyle().Background(theme.Mantle).Foreground(theme.Overlay1).PaddingRight(1).Render("×")
		}

		totalW := lipgloss.Width(mainSeg) + lipgloss.Width(closeSeg)
		if curX+totalW+plusW > w {
			break
		}

		mw := lipgloss.Width(mainSeg)
		cw := lipgloss.Width(closeSeg)
		b.WriteString(mainSeg)
		b.WriteString(closeSeg)
		m.topTabHits = append(m.topTabHits,
			topHit{kind: hitSession, sessionIdx: i, x0: curX, x1: curX + mw},
			topHit{kind: hitSessionClose, sessionIdx: i, x0: curX + mw, x1: curX + mw + cw},
		)
		curX += mw + cw
	}

	if curX+plusW <= w {
		b.WriteString(theme.SpaceBg(theme.Mantle, 1))
		curX++
		pw := lipgloss.Width(plusBtn)
		b.WriteString(plusBtn)
		m.topTabHits = append(m.topTabHits, topHit{kind: hitNewSession, sessionIdx: -1, x0: curX, x1: curX + pw})
		curX += pw
	}

	// Right-aligned subtle shortcut hint when space allows
	shortcutHint := "Alt+1..9  Ctrl+W"
	if w-curX > len(shortcutHint)+4 {
		pad := w - curX - len(shortcutHint) - 1
		b.WriteString(theme.SpaceBg(theme.Mantle, pad))
		b.WriteString(theme.TopBarBg.Render(shortcutHint + " "))
	} else if curX < w {
		b.WriteString(theme.SpaceBg(theme.Mantle, w-curX))
	}

	return theme.TopBarBg.Width(w).MaxHeight(1).Render(components.Truncate(b.String(), w))
}

func sessionTabDot(st views.SessionState, bg lipgloss.Color) string {
	switch st {
	case views.StateConnected:
		return theme.StatusActive.Background(bg).Render("●")
	case views.StateConnecting:
		return theme.StatusStarting.Background(bg).Render("◐")
	case views.StateAuthFailed, views.StateError:
		return theme.StatusError.Background(bg).Render("✖")
	default:
		return theme.StatusStopped.Background(bg).Render("○")
	}
}

func (m *AppModel) renderVaults(w, h int) string {
	lm := m.computeVaultLayout()
	sidebarView := m.renderSidebar(lm.sidebarW, lm.bodyH)

	var rightCol string
	if m.activeTab == TabHosts {
		// Hosts renders edge-to-edge on Layer 0 Crust with its own structured Right Details panel.
		rightCol = m.hostsView.ViewMain(lm.rightW, lm.bodyH)
	} else {
		mainPane := m.activeView().ViewMain(lm.mainInnerW, lm.mainInnerH)
		rightCol = mainPane
		if lm.inspectorOuterH > 0 {
			inspContent := m.activeView().ViewInspector(lm.inspInnerW, lm.inspInnerH)
			inspBox := theme.BorderInactive.
				Padding(0, 1).
				Width(lm.rightW - 2).
				Height(lm.inspectorOuterH - 2).
				MaxWidth(lm.rightW).
				MaxHeight(lm.inspectorOuterH).
				Render(inspContent)
			rightCol = lipgloss.JoinVertical(lipgloss.Left, mainPane, inspBox)
		}
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, sidebarView, rightCol)
}

func (m *AppModel) renderSidebar(outerW, outerH int) string {
	colW := maxInt(8, outerW-1) // reserve 1px vertical divider (#313244 on #181825)

	showWinter := m.showWinterWidget()
	navOuterH := outerH
	winterOuterH := 0
	if showWinter {
		navOuterH = 12 // brand(1) + sep(1) + 6 tabs(6) + spacer(1) + hint(1) + border(2)
		winterOuterH = outerH - navOuterH
	}

	innerW := maxInt(6, colW-2)
	innerH := maxInt(4, navOuterH-2)

	badgeCounts := []int{
		m.counts.Hosts,
		m.counts.Keychain,
		m.counts.PortForwards,
		m.counts.Snippets,
		m.knownHostsView.Count(),
		m.counts.Logs,
	}

	lines := make([]string, 0, innerH)
	ver := strings.TrimPrefix(strings.TrimSpace(m.version), "v")
	if ver == "" {
		ver = "1.0.0"
	}
	brand := theme.BrandStyle.Render(" 󰣀 EZ-SSH") + theme.SpaceBg(theme.Base, 1) + theme.VersionStyle.Render("v"+ver)
	brandPad := maxInt(0, innerW-lipgloss.Width(brand))
	lines = append(lines, components.Truncate(brand, innerW)+theme.SpaceBg(theme.Base, brandPad))
	lines = append(lines, theme.TableSeparator.Render(strings.Repeat("─", innerW)))

	for i, item := range sidebarItems {
		marker := "  "
		if i == m.activeTab {
			marker = "❯ "
		}
		badgeStr := fmt.Sprintf("%d", badgeCounts[i])
		leftText := fmt.Sprintf("%s%s %s", marker, item.icon, item.label)
		badgePad := len(badgeStr) + 1
		leftMax := maxInt(4, innerW-badgePad)
		leftTrunc := components.Truncate(leftText, leftMax)
		gap := innerW - lipgloss.Width(leftTrunc) - len(badgeStr) - 1
		if gap < 1 {
			gap = 1
		}
		rawLine := leftTrunc + strings.Repeat(" ", gap) + badgeStr + " "
		rawLine = components.PadRight(rawLine, innerW)
		if i == m.activeTab {
			lines = append(lines, theme.SidebarItemSelected.Render(rawLine))
		} else {
			lines = append(lines, theme.SidebarItem.Render(rawLine))
		}
	}

	activeTunnels := m.tunnelMgr.ActiveCount()
	for len(lines) < innerH {
		lines = append(lines, theme.SpaceBg(theme.Base, innerW))
	}
	if activeTunnels > 0 && innerH >= 10 {
		txt := components.Truncate(fmt.Sprintf(" ● %d tunnel(s) up", activeTunnels), innerW)
		lines[innerH-1] = lipgloss.NewStyle().Foreground(theme.Green).Background(theme.Base).Bold(true).Render(txt) +
			theme.SpaceBg(theme.Base, maxInt(0, innerW-lipgloss.Width(txt)))
	} else if innerH >= 10 {
		txt := components.Truncate(" [1-6] Nav  •  Tab", innerW)
		lines[innerH-1] = theme.VersionStyle.Render(txt) +
			theme.SpaceBg(theme.Base, maxInt(0, innerW-lipgloss.Width(txt)))
	}

	navBox := theme.NavBoxStyle.
		Width(innerW).
		Height(innerH).
		MaxWidth(colW).
		MaxHeight(navOuterH).
		Render(strings.Join(lines[:innerH], "\n"))

	colStr := navBox
	if showWinter && winterOuterH >= 6 {
		winterBox := m.winter.View(colW, winterOuterH)
		colStr = lipgloss.JoinVertical(lipgloss.Left, navBox, winterBox)
	}

	colLines := strings.Split(colStr, "\n")
	divGlyph := theme.DividerStyle.Render("│")
	out := make([]string, outerH)
	for r := 0; r < outerH; r++ {
		line := ""
		if r < len(colLines) {
			line = colLines[r]
		}
		lw := lipgloss.Width(line)
		if lw < colW {
			line += theme.SpaceBg(theme.Mantle, colW-lw)
		} else if lw > colW {
			line = components.Truncate(line, colW)
		}
		out[r] = line + divGlyph
	}
	return strings.Join(out, "\n")
}

func clampFrame(frame string, maxW, maxH int) string {
	lines := strings.Split(frame, "\n")
	if len(lines) > maxH {
		lines = lines[:maxH]
	}
	for i, l := range lines {
		if lipgloss.Width(l) > maxW {
			lines[i] = components.Truncate(l, maxW)
		}
	}
	return strings.Join(lines, "\n")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func clampInt(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}
