package ui

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	xssh "golang.org/x/crypto/ssh"

	"github.com/ib-jpg/ez-ssh/internal/config"
	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ssh"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
	"github.com/ib-jpg/ez-ssh/internal/ui/views"
)

func newTestApp(t *testing.T) (*AppModel, *config.Config, *db.Store) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("EZ_SSH_CONFIG_DIR", filepath.Join(tmp, "cfg"))
	t.Setenv("EZ_SSH_DIR", filepath.Join(tmp, "ssh"))

	if err := os.MkdirAll(filepath.Join(tmp, "ssh"), 0o700); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	vault, err := crypto.NewVault(cfg.VaultKeyPath)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	store, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := xssh.NewSignerFromKey(priv)
	_ = ssh.AppendKnownHost(cfg.KnownHostsPath, "10.0.1.50:22", nil, signer.PublicKey())

	mgr := ssh.NewTunnelManager(vault, cfg.KnownHostsPath)
	app := NewAppModel(cfg, store, vault, mgr)
	t.Cleanup(app.Shutdown)
	return app, cfg, store
}

func sendKey(app *AppModel, k string) tea.Cmd {
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		msg = tea.KeyMsg{Type: tea.KeyShiftTab}
	case "ctrl+s":
		msg = tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+c":
		msg = tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+w":
		msg = tea.KeyMsg{Type: tea.KeyCtrlW}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "left":
		msg = tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		msg = tea.KeyMsg{Type: tea.KeyRight}
	case "alt+left":
		msg = tea.KeyMsg{Type: tea.KeyLeft, Alt: true}
	case "alt+right":
		msg = tea.KeyMsg{Type: tea.KeyRight, Alt: true}
	case " ":
		msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	default:
		if strings.HasPrefix(k, "alt+") && len(k) == 5 {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(k[4])}, Alt: true}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
	}
	_, cmd := app.Update(msg)
	return cmd
}

func typeText(app *AppModel, s string) {
	for _, r := range s {
		sendKey(app, string(r))
	}
}

func runBatchCmd(app *AppModel, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			ch := make(chan tea.Msg, 1)
			go func(fn tea.Cmd) { ch <- fn() }(sub)
			select {
			case m := <-ch:
				switch m.(type) {
				case nil, spinner.TickMsg, components.ToastExpiredMsg, components.WinterTickMsg, tea.QuitMsg, views.SessionDirtyMsg, views.SessionExitedMsg:
					continue
				default:
					_, next := app.Update(m)
					if next != nil {
						runBatchCmd(app, next)
					}
				}
			case <-time.After(2 * time.Second):
			}
		}
		return
	}
	switch msg.(type) {
	case spinner.TickMsg, components.ToastExpiredMsg, components.WinterTickMsg, tea.QuitMsg, views.SessionDirtyMsg, views.SessionExitedMsg:
		return
	}
	_, next := app.Update(msg)
	if next != nil {
		runBatchCmd(app, next)
	}
}

func assertBounds(t *testing.T, view string, maxW, maxH int, label string) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > maxH {
		t.Fatalf("%s: rendered lines = %d, want <= %d", label, len(lines), maxH)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > maxW {
			t.Fatalf("%s: line %d width = %d > maxW %d (%q)", label, i, w, maxW, l)
		}
	}
}

func TestResponsiveResizeAcrossAllWorkspaces(t *testing.T) {
	app, _, store := newTestApp(t)
	_ = store.CreateHost(&db.Host{
		Name: "edge-ubuntu", Hostname: "10.0.1.12", Port: 22, Username: "ubuntu",
		OSType: "ubuntu", GroupName: "Prod", Tags: "web,nginx",
	})
	_ = store.CreateHost(&db.Host{
		Name: "edge-mikrotik", Hostname: "192.168.88.1", Port: 22, Username: "admin",
		OSType: "mikrotik", GroupName: "Net",
	})
	app.reloadAllViews()

	sizes := [][2]int{
		{40, 12},
		{48, 14},
		{60, 18},
		{80, 24},
		{120, 35},
		{200, 55},
	}

	for _, sz := range sizes {
		app.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		sendKey(app, "alt+1")
		for tab := 0; tab < 6; tab++ {
			app.switchTab(tab)
			assertBounds(t, app.View(), sz[0], sz[1], fmt.Sprintf("vault-tab-%d-%dx%d-on", tab, sz[0], sz[1]))
			sendKey(app, "i")
			assertBounds(t, app.View(), sz[0], sz[1], fmt.Sprintf("vault-tab-%d-%dx%d-off", tab, sz[0], sz[1]))
			sendKey(app, "i")
		}
		sendKey(app, "alt+2")
		assertBounds(t, app.View(), sz[0], sz[1], fmt.Sprintf("sftp-%dx%d", sz[0], sz[1]))
	}
}

func TestEndToEndVaultModulesAndProHostForm(t *testing.T) {
	app, cfg, store := newTestApp(t)
	app.Update(tea.WindowSizeMsg{Width: 110, Height: 34})

	// 1. Keychain CRUD & verify 'q' inside modal does NOT quit.
	sendKey(app, "2")
	if app.activeTab != TabKeychain {
		t.Fatalf("activeTab = %d, want %d", app.activeTab, TabKeychain)
	}
	sendKey(app, "n")
	typeText(app, "qa-cred")
	sendKey(app, "tab") // type selector (Password)
	sendKey(app, "tab") // username
	typeText(app, "deploy")
	sendKey(app, "tab") // password
	typeText(app, "super-pass")
	assertBounds(t, app.View(), 110, 34, "keychain-modal")
	runBatchCmd(app, sendKey(app, "enter"))
	kcs, err := store.ListKeychain()
	if err != nil || len(kcs) != 1 || kcs[0].Name != "qa-cred" {
		t.Fatalf("ListKeychain = (%+v, %v)", kcs, err)
	}

	// 2. Hosts Pro Form CRUD with os_type selector & Search 'q' isolation.
	sendKey(app, "1")
	sendKey(app, "n")
	typeText(app, "prod-ubuntu")
	sendKey(app, "tab") // hostname
	typeText(app, "10.10.0.5")
	sendKey(app, "tab") // port (default 22)
	sendKey(app, "tab") // username
	typeText(app, "ubuntu")
	sendKey(app, "tab")   // OS / Icon selector
	sendKey(app, "right") // Linux -> Ubuntu
	assertBounds(t, app.View(), 110, 34, "pro-host-modal")
	runBatchCmd(app, sendKey(app, "enter"))

	hosts, err := store.ListHosts()
	if err != nil || len(hosts) != 1 || hosts[0].Name != "prod-ubuntu" || hosts[0].OSType != "ubuntu" {
		t.Fatalf("ListHosts = (%+v, %v)", hosts, err)
	}

	// Verify Card Grid and Right Host Details panel render metadata and Connect CTA.
	gridFrame := app.View()
	assertBounds(t, gridFrame, 110, 34, "hosts-card-grid")
	if !strings.Contains(gridFrame, "prod-ubuntu") || !strings.Contains(gridFrame, "10.10.0.5") ||
		!strings.Contains(gridFrame, "Host Details") || !strings.Contains(gridFrame, "Connect") {
		t.Fatalf("expected card grid & right sidebar to show host metadata, Host Details, and Connect CTA")
	}

	// Search with 'q' must filter instead of quitting.
	sendKey(app, "/")
	if !app.activeView().IsSearching() {
		t.Fatal("expected search mode active")
	}
	qCmd := sendKey(app, "q")
	if qCmd != nil {
		if _, isQuit := qCmd().(tea.QuitMsg); isQuit {
			t.Fatal("'q' inside search bar triggered QuitMsg")
		}
	}
	sendKey(app, "esc")

	// 3. Port Forward CRUD
	sendKey(app, "3")
	sendKey(app, "n")
	typeText(app, "pg-fwd")
	sendKey(app, "tab")
	sendKey(app, "tab")
	sendKey(app, "tab")
	sendKey(app, "tab")
	typeText(app, "15432")
	sendKey(app, "tab")
	sendKey(app, "tab")
	typeText(app, "5432")
	runBatchCmd(app, sendKey(app, "enter"))
	pfs, err := store.ListPortForwards()
	if err != nil || len(pfs) != 1 || pfs[0].LocalPort != 15432 {
		t.Fatalf("ListPortForwards = (%+v, %v)", pfs, err)
	}

	// 4. Snippets multi-line CRUD with Ctrl+S
	sendKey(app, "4")
	sendKey(app, "n")
	typeText(app, "disk-check")
	sendKey(app, "tab")
	sendKey(app, "tab")
	typeText(app, "df -h")
	sendKey(app, "enter")
	typeText(app, "free -m")
	runBatchCmd(app, sendKey(app, "ctrl+s"))
	sns, err := store.ListSnippets()
	if err != nil || len(sns) != 1 || !strings.Contains(sns[0].Command, "free -m") {
		t.Fatalf("ListSnippets = (%+v, %v)", sns, err)
	}

	// 5. Known Hosts atomic delete
	sendKey(app, "5")
	if app.knownHostsView.Count() != 1 {
		t.Fatalf("knownHosts count = %d, want 1", app.knownHostsView.Count())
	}
	sendKey(app, "d")
	runBatchCmd(app, sendKey(app, "y"))
	if app.knownHostsView.Count() != 0 {
		t.Fatalf("knownHosts count after delete = %d, want 0", app.knownHostsView.Count())
	}

	// 6. Logs lazy transcript viewer & Clear Old Logs
	transcriptFile := filepath.Join(cfg.LogsDir, "sess.log")
	if err := os.WriteFile(transcriptFile, []byte("\x1b[32mok-output\x1b[0m\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = store.CreateLog(&db.ConnectionLog{
		HostName:       "prod-ubuntu",
		TargetAddr:     "ubuntu@10.10.0.5:22",
		Status:         db.LogStatusSuccess,
		DurationSec:    15,
		TranscriptPath: transcriptFile,
		StartedAt:      time.Now().UTC().AddDate(0, 0, -40),
	})
	sendKey(app, "6")
	sendKey(app, "enter")
	if !app.activeView().IsModalOpen() {
		t.Fatal("expected transcript viewer modal open")
	}
	viewStr := app.View()
	assertBounds(t, viewStr, 110, 34, "logs-transcript-modal")
	if !strings.Contains(viewStr, "ok-output") {
		t.Fatal("expected sanitized transcript content in viewer")
	}
	sendKey(app, "esc")
	sendKey(app, "C")
	runBatchCmd(app, sendKey(app, "y"))
	logs, _ := store.ListLogs("", 100)
	if len(logs) != 0 {
		t.Fatalf("expected logs cleared, got %d", len(logs))
	}

	// 7. Mouse click Vault sidebar tab switching (row 0 = top bar, row 1 = gap; Y=5 -> Hosts, Y=7 -> Port Fwd)
	app.Update(tea.MouseMsg{X: 5, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if app.activeTab != TabHosts {
		t.Fatalf("mouse click tab = %d, want TabHosts", app.activeTab)
	}
	app.Update(tea.MouseMsg{X: 5, Y: 7, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if app.activeTab != TabPortFwd {
		t.Fatalf("mouse click tab = %d, want TabPortFwd", app.activeTab)
	}

	// 8. Normal mode 'q' quits cleanly
	quitCmd := sendKey(app, "q")
	if quitCmd == nil {
		t.Fatal("expected quit cmd on 'q'")
	}
	if _, ok := quitCmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", quitCmd())
	}
}

func TestTopTabBarEmbeddedSessionsAndAuthErrorRecovery(t *testing.T) {
	// Spin up local in-process SSH test server
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := xssh.NewSignerFromKey(hostPriv)
	srvCfg := &xssh.ServerConfig{
		PasswordCallback: func(c xssh.ConnMetadata, pass []byte) (*xssh.Permissions, error) {
			if c.User() == "u1" && string(pass) == "good-pw" {
				return nil, nil
			}
			return nil, errors.New("bad pw")
		},
	}
	srvCfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(conn net.Conn) {
				defer wg.Done()
				defer conn.Close()
				sc, chans, reqs, err := xssh.NewServerConn(conn, srvCfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go xssh.DiscardRequests(reqs)
				for nch := range chans {
					if nch.ChannelType() != "session" {
						_ = nch.Reject(xssh.UnknownChannelType, "unsupported")
						continue
					}
					ch, rqs, err := nch.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer ch.Close()
						for r := range rqs {
							switch r.Type {
							case "pty-req", "window-change":
								if r.WantReply {
									_ = r.Reply(true, nil)
								}
							case "shell":
								if r.WantReply {
									_ = r.Reply(true, nil)
								}
								_, _ = io.WriteString(ch, "WELCOME\r\n")
								br := bufio.NewReader(ch)
								var line []byte
								for {
									b, rerr := br.ReadByte()
									if rerr != nil {
										return
									}
									if b == 0x03 {
										_, _ = io.WriteString(ch, "^C\r\n")
										line = nil
										continue
									}
									if b == '\r' || b == '\n' {
										if len(line) > 0 {
											_, _ = io.WriteString(ch, "echo:"+string(line)+"\r\n")
											line = nil
										}
										continue
									}
									line = append(line, b)
								}
							}
						}
					}()
				}
			}(nc)
		}
	}()

	app, _, store := newTestApp(t)
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	port := ln.Addr().(*net.TCPAddr).Port

	goodEnc, _ := app.vault.Encrypt("good-pw")
	badEnc, _ := app.vault.Encrypt("wrong-pw")
	goodHost := &db.Host{Name: "live-ssh", Hostname: "127.0.0.1", Port: port, Username: "u1", OSType: "ubuntu", InlineSecretEnc: goodEnc}
	badHost := &db.Host{Name: "auth-fail", Hostname: "127.0.0.1", Port: port, Username: "u1", OSType: "debian", InlineSecretEnc: badEnc}
	downHost := &db.Host{Name: "down-node", Hostname: "127.0.0.1", Port: 1, Username: "u1", OSType: "arch", InlineSecretEnc: goodEnc}
	_ = store.CreateHost(goodHost)
	_ = store.CreateHost(badHost)
	_ = store.CreateHost(downHost)
	app.reloadAllViews()

	// 1. Open embedded session for goodHost via double-click or Enter
	cmd := app.openSessionTab(*goodHost, nil)
	runBatchCmd(app, cmd)
	if len(app.sessions) != 1 || app.sessions[0].State != views.StateConnected {
		t.Fatalf("expected StateConnected, got len=%d state=%v", len(app.sessions), app.sessions[0].State)
	}

	// Wait for WELCOME inside VT emulator
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(app.View(), "WELCOME") {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !strings.Contains(app.View(), "WELCOME") {
		t.Fatalf("expected WELCOME in terminal view:\n%s", app.View())
	}

	// Dragging the mouse inside the shell must produce a visible selection and
	// copy its text on release.
	app.Update(tea.MouseMsg{X: 0, Y: 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	app.Update(tea.MouseMsg{X: 8, Y: 2, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	sw, sh := app.sessionViewportSize()
	selectedFrame := app.sessions[0].View(sw, sh)
	_, copyCmd := app.Update(tea.MouseMsg{X: 8, Y: 2, Action: tea.MouseActionRelease, Button: tea.MouseButtonNone})
	if copyCmd == nil {
		t.Fatal("expected copy result command after releasing mouse selection")
	}
	if selectedFrame == app.sessions[0].View(sw, sh) {
		t.Fatal("expected visible selection highlight during mouse drag")
	}

	// Ctrl+C while StateConnected MUST NOT quit; it must forward ^C to remote shell!
	ctrlCCmd := sendKey(app, "ctrl+c")
	if ctrlCCmd != nil {
		if _, isQuit := ctrlCCmd().(tea.QuitMsg); isQuit {
			t.Fatal("Ctrl+C in StateConnected quit the app instead of forwarding to PTY")
		}
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(app.View(), "^C") {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !strings.Contains(app.View(), "^C") {
		t.Fatal("expected ^C echoed back from remote PTY")
	}

	// Bracketed paste from the outer terminal reaches the embedded shell.
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Paste: true, Runes: []rune("paste-line")})
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(app.View(), "echo:paste-line") {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !strings.Contains(app.View(), "echo:paste-line") {
		t.Fatalf("expected pasted text in terminal view:\n%s", app.View())
	}

	// Copy/paste shortcuts stay inside the session instead of quitting the app.
	for _, k := range []string{"alt+c", "ctrl+v", "alt+v"} {
		cmd := sendKey(app, k)
		if cmd != nil {
			if _, isQuit := cmd().(tea.QuitMsg); isQuit {
				t.Fatalf("%s inside connected session quit the app", k)
			}
		}
	}
	if app.activeWS != wsSession {
		t.Fatalf("copy/paste keys changed workspace to %d", app.activeWS)
	}

	// Switch Alt+1 (Vaults), Alt+2 (SFTP), Alt+3 (Session) — background session stays connected!
	sendKey(app, "alt+1")
	if app.activeWS != wsVaults {
		t.Fatalf("Alt+1 activeWS = %d", app.activeWS)
	}
	sendKey(app, "alt+2")
	if app.activeWS != wsSFTP {
		t.Fatalf("Alt+2 activeWS = %d", app.activeWS)
	}
	sendKey(app, "alt+3")
	if app.activeWS != wsSession || app.sessions[0].State != views.StateConnected {
		t.Fatalf("Alt+3 activeWS=%d state=%v", app.activeWS, app.sessions[0].State)
	}

	// 2. Wrong password -> StateAuthFailed
	runBatchCmd(app, app.openSessionTab(*badHost, nil))
	if app.sessions[1].State != views.StateAuthFailed {
		t.Fatalf("badHost state = %v, want StateAuthFailed", app.sessions[1].State)
	}
	authFrame := app.View()
	assertBounds(t, authFrame, 100, 30, "auth-failed-card")
	if !strings.Contains(authFrame, "Authentication Failed") {
		t.Fatalf("expected Authentication Failed card")
	}

	// 3. Unreachable host -> StateError -> 'e' jumps to Vaults Edit Modal!
	runBatchCmd(app, app.openSessionTab(*downHost, nil))
	if app.sessions[2].State != views.StateError {
		t.Fatalf("downHost state = %v, want StateError", app.sessions[2].State)
	}
	assertBounds(t, app.View(), 100, 30, "error-card")
	runBatchCmd(app, sendKey(app, "e"))
	if app.activeWS != wsVaults || app.activeTab != TabHosts || !app.hostsView.IsModalOpen() {
		t.Fatalf("expected 'e' on StateError to open Edit Host modal in Vaults")
	}
	sendKey(app, "esc")

	// Close all 3 sessions via Alt+3 + Ctrl+W
	sendKey(app, "alt+3")
	sendKey(app, "ctrl+w")
	sendKey(app, "ctrl+w")
	sendKey(app, "ctrl+w")
	if len(app.sessions) != 0 || app.activeWS != wsVaults {
		t.Fatalf("expected all sessions closed, got len=%d ws=%d", len(app.sessions), app.activeWS)
	}
}

func TestSFTPDualPaneLocalFlows(t *testing.T) {
	app, _, _ := newTestApp(t)
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	workDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(workDir, ".secret"), []byte("hidden"), 0o600)
	_ = os.WriteFile(filepath.Join(workDir, "readme.txt"), []byte("hi"), 0o644)

	app.sftpView.SetLocalDir(workDir)
	sendKey(app, "alt+2")
	if app.activeWS != wsSFTP {
		t.Fatalf("expected SFTP workspace")
	}

	// Toggle hidden dotfiles with '.'
	before := app.sftpView.LocalEntriesCount()
	sendKey(app, ".")
	after := app.sftpView.LocalEntriesCount()
	if after != before+1 {
		t.Fatalf("hidden toggle counts: before=%d after=%d", before, after)
	}

	// Switch pane with Tab and back
	sendKey(app, "tab")
	if app.sftpView.ActivePane() != 1 {
		t.Fatalf("expected remote pane focused, got %d", app.sftpView.ActivePane())
	}
	sendKey(app, "tab")

	// Mkdir 'qdir' (ensure 'q' inside modal does NOT quit!), rename to 'rdir', delete it
	sendKey(app, "m")
	typeText(app, "qdir")
	sendKey(app, "enter")
	if _, err := os.Stat(filepath.Join(workDir, "qdir")); err != nil {
		t.Fatalf("expected qdir created: %v", err)
	}

	// Navigate down to qdir (".." then "qdir")
	sendKey(app, "j")
	sendKey(app, "r")
	for i := 0; i < 4; i++ {
		_, _ = app.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	typeText(app, "rdir")
	sendKey(app, "enter")
	if _, err := os.Stat(filepath.Join(workDir, "rdir")); err != nil {
		t.Fatalf("expected rdir renamed: %v", err)
	}

	sendKey(app, "x")
	sendKey(app, "y")
	if _, err := os.Stat(filepath.Join(workDir, "rdir")); !os.IsNotExist(err) {
		t.Fatalf("expected rdir deleted, stat err=%v", err)
	}
}

func TestHostSSHConfigImportAndSecretPreservationAndKeygen(t *testing.T) {
	app, cfg, store := newTestApp(t)
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	privPath, _, _, err := ssh.GenerateKey(cfg.SSHDir, "id_import_test", "imp@test", ssh.AlgoEd25519, "")
	if err != nil {
		t.Fatal(err)
	}
	sshCfg := "Host imported-web\n    HostName 192.168.10.20\n    User webop\n    Port 2200\n    IdentityFile " + privPath + "\n"
	if err := os.WriteFile(cfg.SSHConfigPath, []byte(sshCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	runBatchCmd(app, sendKey(app, "I"))
	hosts, _ := store.ListHosts()
	if len(hosts) != 1 || hosts[0].Name != "imported-web" || hosts[0].KeychainID == nil {
		t.Fatalf("imported hosts = %+v", hosts)
	}
	sendKey(app, "I")
	hosts2, _ := store.ListHosts()
	if len(hosts2) != 1 {
		t.Fatalf("duplicate import created extra host: %d", len(hosts2))
	}

	pwEnc, _ := app.vault.Encrypt("keep-me-pw")
	pwHost := &db.Host{
		Name:            "pw-node",
		Hostname:        "10.0.0.8",
		Port:            22,
		Username:        "root",
		OSType:          "debian",
		InlineSecretEnc: pwEnc,
	}
	if err := store.CreateHost(pwHost); err != nil {
		t.Fatal(err)
	}
	app.reloadAllViews()

	sendKey(app, "right")
	sendKey(app, "e")
	runBatchCmd(app, sendKey(app, "enter"))
	updated, err := store.GetHost(pwHost.ID)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := app.vault.Decrypt(updated.InlineSecretEnc)
	if err != nil || dec != "keep-me-pw" {
		t.Fatalf("inline password after blank edit = (%q, %v), want keep-me-pw", dec, err)
	}

	sendKey(app, "2")
	sendKey(app, "g")
	typeText(app, "auto-gen")
	runBatchCmd(app, sendKey(app, "enter"))
	kcs, _ := store.ListKeychain()
	foundGen := false
	for _, k := range kcs {
		if k.Name == "auto-gen" && k.Type == db.KeychainSSHKey && strings.HasPrefix(k.PublicKey, "ssh-ed25519 ") {
			foundGen = true
		}
	}
	if !foundGen {
		t.Fatalf("expected generated key in keychain, got %+v", kcs)
	}
}

func TestHostsModalRoundedInputsNoAnsiBleedAndBounds(t *testing.T) {
	origProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(origProfile)

	// 1. Verify RenderRoundedInputBox directly under empty/focused/short/overflow states.
	for _, tc := range []struct {
		name    string
		focused bool
		value   string
		outerW  int
	}{
		{"empty-blurred", false, "", 32},
		{"empty-focused", true, "", 32},
		{"short-value", true, "prod-web-01", 38},
		{"overflow-value", true, strings.Repeat("x", 80), 28},
	} {
		ti := views.NewStyledInput("Find or enter value...", 128)
		if tc.focused {
			ti.Focus()
		}
		if tc.value != "" {
			ti.SetValue(tc.value)
			ti.CursorEnd()
		}
		rendered := views.RenderRoundedInputBox(&ti, tc.outerW, tc.focused, theme.Base, theme.Mantle)
		lines := strings.Split(rendered, "\n")
		if len(lines) != 3 {
			t.Fatalf("%s: expected 3 lines for rounded input box, got %d", tc.name, len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w != tc.outerW {
				t.Fatalf("%s: line %d width = %d, want %d (%q)", tc.name, i, w, tc.outerW, l)
			}
		}
		plainTop := ansi.Strip(lines[0])
		plainBot := ansi.Strip(lines[2])
		if !strings.HasPrefix(plainTop, "╭") || !strings.HasSuffix(plainTop, "╮") {
			t.Fatalf("%s: expected rounded top border, got %q", tc.name, plainTop)
		}
		if !strings.HasPrefix(plainBot, "╰") || !strings.HasSuffix(plainBot, "╯") {
			t.Fatalf("%s: expected rounded bottom border, got %q", tc.name, plainBot)
		}
		// Ensure zero raw unstyled space runs (\x1b[0m  ) inside middle content line.
		if strings.Contains(lines[1], "\x1b[0m  ") {
			t.Fatalf("%s: found unstyled space bleed after ANSI reset in input row: %q", tc.name, lines[1])
		}
	}

	// 2. Verify full New SSH Host modal appearance, Auth Mode cycling, typing, and responsive bounds.
	app, _, _ := newTestApp(t)
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 36})

	sendKey(app, "n")
	modalFrame := app.View()
	assertBounds(t, modalFrame, 120, 36, "new-host-modal-120x36")

	plain := ansi.Strip(modalFrame)
	if strings.ContainsAny(plain, "┌┐└┘") {
		t.Fatalf("expected zero NormalBorder corners in redesigned New SSH Host modal")
	}
	for _, want := range []string{
		"New SSH Host",
		"Name *",
		"Host / IP *",
		"Port",
		"Username *",
		"OS Icon",
		"Auth Mode",
		"Password",
		"Keychain",
		"SSH Key",
		"Agent",
		"Group",
		"Tags",
		"Record session logs",
		"Cancel (Esc)",
		"Save Host (Ctrl+S)",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected New SSH Host modal to contain %q\n%s", want, plain)
		}
	}
	// Ensure no unstyled space bleed (\x1b[0m  +) inside the modal frame.
	if strings.Contains(modalFrame, "\x1b[0m    ") {
		t.Fatalf("unexpected large unstyled space run inside TrueColor modal frame")
	}

	// Cycle fields and verify Auth Mode cycles Password -> Keychain -> SSH Key -> Agent -> Password.
	typeText(app, "staging-api")
	sendKey(app, "tab") // Host / IP
	typeText(app, "10.20.30.40")
	sendKey(app, "tab") // Port
	sendKey(app, "tab") // Username
	typeText(app, "deploy")
	sendKey(app, "tab") // OS Icon
	sendKey(app, "right")
	sendKey(app, "tab")   // Auth Mode (starts at Password)
	sendKey(app, "right") // -> Keychain
	if p := ansi.Strip(app.View()); !strings.Contains(p, "Keychain Credential") {
		t.Fatalf("expected Keychain Credential box after right arrow on Auth Mode")
	}
	sendKey(app, "right") // -> SSH Key
	if p := ansi.Strip(app.View()); !strings.Contains(p, "Key Path *") {
		t.Fatalf("expected Key Path * box after right arrow on Auth Mode")
	}
	sendKey(app, "right") // -> Agent
	if p := ansi.Strip(app.View()); !strings.Contains(p, "SSH_AUTH_SOCK") {
		t.Fatalf("expected Agent hint after right arrow on Auth Mode")
	}
	sendKey(app, "right") // -> Password again
	sendKey(app, "tab")   // Password input
	typeText(app, "s3cr3t")
	sendKey(app, "tab") // Group
	sendKey(app, "tab") // Tags
	typeText(app, "api,v2")
	sendKey(app, "tab") // Record toggle
	sendKey(app, " ")   // Toggle recording ON
	if p := ansi.Strip(app.View()); !strings.Contains(p, "[✓]") {
		t.Fatalf("expected [✓] Record session logs when toggled on")
	}

	for _, sz := range [][2]int{{48, 14}, {60, 18}, {80, 24}, {110, 34}, {120, 36}, {200, 55}} {
		app.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		assertBounds(t, app.View(), sz[0], sz[1], fmt.Sprintf("host-modal-%dx%d", sz[0], sz[1]))
	}
	sendKey(app, "esc")
}

func TestVerticalBreathingRoomLayoutAndHostForm(t *testing.T) {
	app, _, store := newTestApp(t)
	const w, h = 120, 50
	app.Update(tea.WindowSizeMsg{Width: w, Height: h})

	for i := 0; i < 4; i++ {
		_ = store.CreateHost(&db.Host{
			Name: fmt.Sprintf("box-%d", i), Hostname: fmt.Sprintf("10.0.0.%d", i+1),
			Port: 22, Username: "ops", OSType: "ubuntu",
		})
	}
	app.reloadAllViews()
	assertBounds(t, app.View(), w, h, "hosts-with-gaps")

	lines := strings.Split(ansi.Strip(app.View()), "\n")
	blank := strings.Repeat(" ", w)
	if lines[1] != blank {
		t.Fatalf("expected breathing room below top bar, got %q", lines[1])
	}
	if lines[len(lines)-2] != blank {
		t.Fatalf("expected breathing room above footer, got %q", lines[len(lines)-2])
	}

	// The New SSH Host form separates field groups with one blank row on a
	// terminal tall enough to still show every field.
	sendKey(app, "n")
	formLines := strings.Split(ansi.Strip(app.View()), "\n")
	nameIdx := -1
	for i, l := range formLines {
		if strings.Contains(l, "Name *") {
			nameIdx = i
			break
		}
	}
	if nameIdx < 0 || nameIdx+4 >= len(formLines) {
		t.Fatalf("Name label not found in form:\n%s", strings.Join(formLines, "\n"))
	}
	if strings.Trim(formLines[nameIdx+4], " │") != "" {
		t.Fatalf("expected blank row between Name and Host groups, got %q", formLines[nameIdx+4])
	}
	assertBounds(t, app.View(), w, h, "host-form-with-gaps")
	sendKey(app, "esc")
}

func TestHostsTermiusHierarchyDropdownAndWinterSidebar(t *testing.T) {
	app, _, store := newTestApp(t)
	_ = store.CreateHost(&db.Host{
		Name: "alpha-box", Hostname: "10.0.1.10", Port: 22, Username: "ubuntu",
		OSType: "ubuntu", GroupName: "Prod", Tags: "web",
	})
	_ = store.CreateHost(&db.Host{
		Name: "beta-box", Hostname: "10.0.1.20", Port: 22, Username: "arch",
		OSType: "arch", GroupName: "Prod", Tags: "worker",
	})
	app.reloadAllViews()
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 36})

	// 1. Full-width search bar ad-hoc user@host:port connect
	sendKey(app, "/")
	typeText(app, "ops@10.9.8.7:2222")
	if cmd := sendKey(app, "enter"); cmd != nil {
		app.Update(cmd())
	}
	if len(app.sessions) != 1 || app.sessions[0].Host.Username != "ops" ||
		app.sessions[0].Host.Hostname != "10.9.8.7" || app.sessions[0].Host.Port != 2222 {
		t.Fatalf("expected ad-hoc session ops@10.9.8.7:2222, got %+v", app.sessions)
	}
	sendKey(app, "ctrl+w")
	sendKey(app, "alt+1")

	// Clear search query
	sendKey(app, "/")
	sendKey(app, "esc")

	// 2. Quick Terminal modal
	sendKey(app, "t")
	if !app.hostsView.IsModalOpen() {
		t.Fatal("expected Quick Terminal modal open")
	}
	typeText(app, "dev@10.2.3.4:2200")
	if cmd := sendKey(app, "enter"); cmd != nil {
		app.Update(cmd())
	}
	if len(app.sessions) != 1 || app.sessions[0].Host.Username != "dev" ||
		app.sessions[0].Host.Hostname != "10.2.3.4" || app.sessions[0].Host.Port != 2200 {
		t.Fatalf("expected Quick Terminal session dev@10.2.3.4:2200, got %+v", app.sessions)
	}
	sendKey(app, "ctrl+w")
	sendKey(app, "alt+1")

	// 3. Floating Context Dropdown: keyboard 'm', arrow nav, Esc close, right-click open, outside-click close, Duplicate 'c'
	sendKey(app, "m")
	if !app.hostsView.IsDropdownOpen() {
		t.Fatal("expected floating dropdown open on 'm'")
	}
	dropFrame := ansi.Strip(app.View())
	for _, item := range []string{"Quick Connect", "Open SFTP", "Edit Host Details", "Duplicate", "Remove"} {
		if !strings.Contains(dropFrame, item) {
			t.Fatalf("expected dropdown item %q", item)
		}
	}
	assertBounds(t, app.View(), 120, 36, "hosts-dropdown-open")
	sendKey(app, "down")
	sendKey(app, "esc")
	if app.hostsView.IsDropdownOpen() {
		t.Fatal("expected Esc to close dropdown")
	}

	// Right-click on card 0 (card area inside HostsView: X=35, Y=8), then click outside (X=2, Y=2)
	app.Update(tea.MouseMsg{X: 35, Y: 8, Action: tea.MouseActionPress, Button: tea.MouseButtonRight})
	if !app.hostsView.IsDropdownOpen() {
		t.Fatal("expected right-click on host card to open floating dropdown")
	}
	app.Update(tea.MouseMsg{X: 2, Y: 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if app.hostsView.IsDropdownOpen() {
		t.Fatal("expected click outside dropdown to dismiss it")
	}

	// Duplicate ('c') adds a copy
	sendKey(app, "c")
	hostsAfterDup, _ := store.ListHosts()
	if len(hostsAfterDup) != 3 {
		t.Fatalf("hosts count after Duplicate = %d, want 3", len(hostsAfterDup))
	}
	app.toast = components.ToastModel{}

	// 4. Right Host Details panel [ ⇥ ] collapse click + 'i' restore + full-width Connect button click
	if !strings.Contains(app.View(), "Host Details") {
		t.Fatal("expected Host Details right panel visible initially")
	}
	if _, cmd := app.Update(tea.MouseMsg{X: 117, Y: 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}); cmd != nil {
		app.Update(cmd())
	}
	if strings.Contains(app.View(), "Host Details") {
		t.Fatal("expected [ ⇥ ] click to collapse Host Details panel")
	}
	sendKey(app, "i")
	if !strings.Contains(app.View(), "Host Details") {
		t.Fatal("expected 'i' to restore Host Details panel")
	}
	if _, cmd := app.Update(tea.MouseMsg{X: 100, Y: 33, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}); cmd != nil {
		app.Update(cmd())
	}
	if len(app.sessions) != 1 {
		t.Fatalf("expected full-width Connect CTA click to open session tab, got %d sessions", len(app.sessions))
	}
	sendKey(app, "ctrl+w")
	sendKey(app, "alt+1")

	// 5. Split Left Sidebar & Winter Widget lifecycle (30 rows visible, <26 hidden, Alt+2 stops ticker)
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if !app.showWinterWidget() {
		t.Fatal("expected winter widget visible at height 30")
	}
	if !strings.Contains(ansi.Strip(app.View()), "EZ-SSH") {
		t.Fatal("expected EZ-SSH banner in winter widget at height 30")
	}
	_, tickNext := app.Update(components.WinterTickMsg{Gen: app.winterTickGen})
	if tickNext == nil {
		t.Fatal("expected active WinterTickMsg on Vaults to schedule next tick")
	}

	sendKey(app, "alt+2")
	if app.winterRunning {
		t.Fatal("expected winter ticker to stop when switching to SFTP")
	}
	_, staleNext := app.Update(components.WinterTickMsg{Gen: app.winterTickGen})
	if staleNext != nil {
		t.Fatal("expected stale WinterTickMsg on SFTP to return nil cmd")
	}

	sendKey(app, "alt+1")
	if !app.winterRunning {
		t.Fatal("expected winter ticker to resume when returning to Vaults")
	}

	app.Update(tea.WindowSizeMsg{Width: 120, Height: 22})
	if app.showWinterWidget() {
		t.Fatal("expected winter widget hidden when height < 26")
	}

	for _, sz := range [][2]int{{48, 14}, {60, 18}, {80, 24}, {100, 28}, {120, 35}, {200, 55}} {
		app.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		assertBounds(t, app.View(), sz[0], sz[1], fmt.Sprintf("hosts-layout-%dx%d", sz[0], sz[1]))
	}
}

func TestSecondaryVaultViewsHeaderSearchAndNewButton(t *testing.T) {
	app, cfg, store := newTestApp(t)
	enc, _ := app.vault.Encrypt("secret-pw")
	_ = store.CreateKeychain(&db.KeychainItem{Name: "kc-one", Type: db.KeychainPassword, Username: "ops", SecretEnc: enc})
	_ = store.CreateKeychain(&db.KeychainItem{Name: "kc-two", Type: db.KeychainPassword, Username: "admin", SecretEnc: enc})
	h := &db.Host{Name: "web-1", Hostname: "10.0.0.5", Port: 22, Username: "ubuntu", OSType: "ubuntu"}
	_ = store.CreateHost(h)
	_ = store.CreatePortForward(&db.PortForward{Name: "pf-1", HostID: h.ID, Type: db.TunnelLocal, LocalAddr: "127.0.0.1", LocalPort: 8080, RemoteAddr: "127.0.0.1", RemotePort: 80})
	_ = store.CreateSnippet(&db.Snippet{Name: "sn-1", Category: "Ops", Command: "uptime"})
	_ = store.CreateLog(&db.ConnectionLog{HostName: "web-1", TargetAddr: "ubuntu@10.0.0.5:22", Status: db.LogStatusSuccess, DurationSec: 5, StartedAt: time.Now().UTC()})
	_ = cfg
	app.reloadAllViews()

	const w, hTerm = 120, 36
	app.Update(tea.WindowSizeMsg{Width: w, Height: hTerm})

	cases := []struct {
		key       string
		tab       int
		dupTitle  string
		expectNew bool
		placeholder string
	}{
		{"2", TabKeychain, "󰌆 Keychain", true, "Search keychain..."},
		{"3", TabPortFwd, "󰛳 Port Forwarding", true, "Search tunnels..."},
		{"4", TabSnippets, "󰆍 Snippets", true, "Search snippets..."},
		{"5", TabKnownHosts, "󰢬 Known Hosts", false, "Search host / fingerprint..."},
		{"6", TabLogs, "󱂅 Logs", false, "Search logs..."},
	}

	lm := app.computeVaultLayout()
	for _, tc := range cases {
		sendKey(app, tc.key)
		if app.activeTab != tc.tab {
			t.Fatalf("expected activeTab %d, got %d", tc.tab, app.activeTab)
		}

		// Inspect the right workspace pane directly (ViewMain) to verify no duplicate title.
		rightPane := ansi.Strip(app.activeView().ViewMain(lm.mainInnerW, lm.mainInnerH))
		firstLine := strings.Split(rightPane, "\n")[0]
		if strings.Count(firstLine, "╭") != 1 || strings.Count(firstLine, "╮") != 1 {
			t.Fatalf("tab %d header should be one unified rounded box, got top border %q", tc.tab, firstLine)
		}
		if strings.Contains(rightPane, tc.dupTitle) {
			t.Fatalf("tab %d still renders duplicate title %q in right workspace:\n%s", tc.tab, tc.dupTitle, rightPane)
		}
		if !strings.Contains(rightPane, "󰍉") || !strings.Contains(rightPane, tc.placeholder) {
			t.Fatalf("tab %d missing stretched search bar (%q):\n%s", tc.tab, tc.placeholder, rightPane)
		}
		hasNew := strings.Contains(rightPane, "+ New")
		if hasNew != tc.expectNew {
			t.Fatalf("tab %d has '+ New' = %v, want %v:\n%s", tc.tab, hasNew, tc.expectNew, rightPane)
		}
		assertBounds(t, app.View(), w, hTerm, fmt.Sprintf("secondary-view-%d", tc.tab))
	}

	// Verify mouse click on stretched Search bar, table row 1, and "+ New" button in Keychain.
	sendKey(app, "2")
	app.Update(tea.MouseMsg{X: 30, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !app.activeView().IsSearching() {
		t.Fatal("expected mouse click on stretched search bar to activate search")
	}

	// Clicking table row 1 (adjMsg.Y = TableTopOffset+1 => screen Y = 2+6+1 = 9) blurs search and selects row 1.
	app.Update(tea.MouseMsg{X: 35, Y: 9, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if app.activeView().IsSearching() {
		t.Fatal("expected clicking outside search header to blur search")
	}

	// Clicking "+ New" button (top right of header bar: X=115, Y=3) opens New modal.
	app.Update(tea.MouseMsg{X: 115, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !app.activeView().IsModalOpen() {
		t.Fatal("expected clicking '+ New' button to open New Keychain modal")
	}
	sendKey(app, "esc")

	// Also verify "+ New" click works on Port Fwd and Snippets.
	for _, k := range []string{"3", "4"} {
		sendKey(app, k)
		app.Update(tea.MouseMsg{X: 115, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		if !app.activeView().IsModalOpen() {
			t.Fatalf("tab key %s: expected clicking '+ New' button to open modal", k)
		}
		sendKey(app, "esc")
	}
}

func TestHostModalAuthModeHasGapBeforePasswordLabel(t *testing.T) {
	app, _, store := newTestApp(t)
	pwEnc, _ := app.vault.Encrypt("saved-secret")
	_ = store.CreateHost(&db.Host{
		Name: "pw-host", Hostname: "10.0.0.9", Port: 22, Username: "ubuntu",
		OSType: "ubuntu", InlineSecretEnc: pwEnc,
	})
	app.reloadAllViews()

	for _, sz := range [][2]int{{120, 36}, {120, 50}} {
		app.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})

		// 1. New Host modal (Password mode)
		sendKey(app, "n")
		newPlain := ansi.Strip(app.View())
		assertBlankRowBetween(t, newPlain, "Auth Mode", "Password *", fmt.Sprintf("new-host-%dx%d", sz[0], sz[1]))
		sendKey(app, "esc")

		// 2. Edit Host modal (Password mode with saved password)
		sendKey(app, "e")
		editPlain := ansi.Strip(app.View())
		assertBlankRowBetween(t, editPlain, "Auth Mode", "Password (leave blank to keep saved)", fmt.Sprintf("edit-host-%dx%d", sz[0], sz[1]))
		sendKey(app, "esc")
	}
}

func assertBlankRowBetween(t *testing.T, plain, upperMarker, lowerMarker, label string) {
	t.Helper()
	lines := strings.Split(plain, "\n")
	upperIdx, lowerIdx := -1, -1
	for i, l := range lines {
		if upperIdx < 0 && strings.Contains(l, upperMarker) {
			upperIdx = i
		} else if upperIdx >= 0 && lowerIdx < 0 && strings.Contains(l, lowerMarker) {
			lowerIdx = i
			break
		}
	}
	if upperIdx < 0 || lowerIdx < 0 {
		t.Fatalf("%s: markers %q / %q not found:\n%s", label, upperMarker, lowerMarker, plain)
	}
	// Auth Mode row is upperIdx, pill row is upperIdx+1, blank gap row must be upperIdx+2, and lowerMarker at upperIdx+3.
	if lowerIdx != upperIdx+3 {
		t.Fatalf("%s: expected 1 blank row between Auth Mode pills and %q (upperIdx=%d, lowerIdx=%d):\n%s",
			label, lowerMarker, upperIdx, lowerIdx, strings.Join(lines[upperIdx:lowerIdx+1], "\n"))
	}
	gapRow := strings.Trim(lines[upperIdx+2], " │")
	if gapRow != "" {
		t.Fatalf("%s: expected blank row at line %d, got %q", label, upperIdx+2, lines[upperIdx+2])
	}
}

func TestAppSetVersionUpdatesSidebar(t *testing.T) {
	app, _, _ := newTestApp(t)
	app.Update(tea.WindowSizeMsg{Width: 110, Height: 32})
	app.SetVersion("v1.0.2")
	plain := ansi.Strip(app.View())
	if !strings.Contains(plain, "v1.0.2") || strings.Contains(plain, "vv1.0.2") {
		t.Fatalf("expected sidebar to display normalized v1.0.2:\n%s", plain)
	}
}
