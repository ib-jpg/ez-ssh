package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/vt"
	"github.com/muesli/termenv"

	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/views"
)

// TestNoDefaultBackgroundCells is a diagnostic guard: every cell of the
// rendered TUI must carry an explicit background color so the terminal's
// default (gray) background is never exposed.
func TestNoDefaultBackgroundCells(t *testing.T) {
	origProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(origProfile)

	app, cfg, store := newTestApp(t)
	var firstHost db.Host
	for i := 0; i < 5; i++ {
		h := &db.Host{
			Name:      fmt.Sprintf("node-%d", i),
			Hostname:  fmt.Sprintf("10.0.1.%d", 10+i),
			Port:      22,
			Username:  "ubuntu",
			OSType:    "ubuntu",
			GroupName: "Prod",
			Tags:      "web",
		}
		if err := store.CreateHost(h); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstHost = *h
		}
	}

	enc, err := app.vault.Encrypt("s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.CreateKeychain(&db.KeychainItem{
		Name: "prod-pass", Type: db.KeychainPassword, Username: "ubuntu", SecretEnc: enc,
	})
	_ = store.CreateKeychain(&db.KeychainItem{
		Name: "deploy-key", Type: db.KeychainSSHKey, Username: "ubuntu",
		PrivateKeyPath: filepath.Join(cfg.SSHDir, "id_ed25519"), PublicKey: "ssh-ed25519 AAAA test",
	})
	_ = store.CreatePortForward(&db.PortForward{
		Name: "pg-tunnel", HostID: firstHost.ID, Type: db.TunnelLocal,
		LocalAddr: "127.0.0.1", LocalPort: 15432, RemoteAddr: "127.0.0.1", RemotePort: 5432,
	})
	_ = store.CreateSnippet(&db.Snippet{
		Name: "sys-check", Category: "Ops", Command: "uname -a\ndf -h",
	})
	transcriptPath := filepath.Join(cfg.LogsDir, "audit.log")
	if err := os.WriteFile(transcriptPath, []byte("audit line 1\r\naudit line 2\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = store.CreateLog(&db.ConnectionLog{
		HostName: "node-0", TargetAddr: "ubuntu@10.0.1.10:22",
		Status: db.LogStatusSuccess, DurationSec: 12,
		TranscriptPath: transcriptPath, StartedAt: time.Now().UTC(),
	})
	app.reloadAllViews()

	sizes := [][2]int{{200, 50}, {140, 40}, {120, 36}, {100, 30}, {80, 24}}
	for _, sz := range sizes {
		app.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		sendKey(app, "alt+1")

		// 1. All 6 Vault tabs with inspector ON/OFF + active search mode
		for tab := 0; tab < 6; tab++ {
			app.switchTab(tab)
			for _, inspector := range []bool{true, false} {
				if !inspector {
					sendKey(app, "i")
				}
				checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-tab%d-inspector=%v", sz[0], sz[1], tab, inspector))
				if !inspector {
					sendKey(app, "i")
				}
			}
			sendKey(app, "/")
			checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-tab%d-search-empty", sz[0], sz[1], tab))
			typeText(app, "a")
			checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-tab%d-search-typed", sz[0], sz[1], tab))
			sendKey(app, "esc")
		}

		// 2. All modals, dropdowns, transcript viewer, confirm, and toast overlays
		sendKey(app, "1")
		sendKey(app, "n")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-host-modal", sz[0], sz[1]))
		sendKey(app, "esc")
		sendKey(app, "m")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-host-dropdown", sz[0], sz[1]))
		sendKey(app, "esc")
		sendKey(app, "t")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-quick-term", sz[0], sz[1]))
		sendKey(app, "esc")
		sendKey(app, "d")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-host-confirm", sz[0], sz[1]))
		sendKey(app, "esc")

		sendKey(app, "2")
		sendKey(app, "n")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-keychain-modal", sz[0], sz[1]))
		sendKey(app, "esc")
		sendKey(app, "g")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-keychain-gen-modal", sz[0], sz[1]))
		sendKey(app, "esc")

		sendKey(app, "3")
		sendKey(app, "n")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-portfwd-modal", sz[0], sz[1]))
		sendKey(app, "esc")

		sendKey(app, "4")
		sendKey(app, "n")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-snippet-modal", sz[0], sz[1]))
		typeText(app, "ls")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-snippet-modal-typed", sz[0], sz[1]))
		sendKey(app, "esc")

		sendKey(app, "6")
		sendKey(app, "enter")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-logs-viewer", sz[0], sz[1]))
		sendKey(app, "esc")

		app.toast.Show(components.ToastSuccess, "Saved host")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-toast", sz[0], sz[1]))
		app.toast.Update(components.ToastExpiredMsg{ID: 1})

		// 3. SFTP workspace + host picker + mkdir modal
		sendKey(app, "alt+2")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-sftp", sz[0], sz[1]))
		sendKey(app, "c")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-sftp-picker", sz[0], sz[1]))
		sendKey(app, "esc")
		sendKey(app, "m")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-sftp-mkdir", sz[0], sz[1]))
		sendKey(app, "esc")

		// 4. Embedded SSH session tabs (connecting, auth-failed, error cards + top bar tabs)
		app.Update(views.OpenSessionMsg{Host: firstHost})
		sess1 := app.sessions[len(app.sessions)-1]
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-session-connecting", sz[0], sz[1]))

		app.Update(views.SessionConnectResultMsg{
			TabID: sess1.ID, AttemptID: 0, Err: errors.New("ssh: unable to authenticate"),
		})
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-session-authfailed", sz[0], sz[1]))

		app.Update(views.OpenSessionMsg{Host: firstHost})
		sess2 := app.sessions[len(app.sessions)-1]
		app.Update(views.SessionConnectResultMsg{
			TabID: sess2.ID, AttemptID: 0, Err: errors.New("dial tcp: connection refused"),
		})
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-session-error", sz[0], sz[1]))

		for len(app.sessions) > 0 {
			sendKey(app, "ctrl+w")
		}
		sendKey(app, "alt+1")
	}

	app.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	checkBg(t, app.View(), 40, 12, "40x12-too-small")

	for _, sz := range [][2]int{{80, 24}, {120, 36}} {
		app.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		app.EnableIntro("1.0")
		checkBg(t, app.View(), sz[0], sz[1], fmt.Sprintf("%dx%d-intro-splash", sz[0], sz[1]))
		app.Update(introDoneMsg{})
	}
}

func checkBg(t *testing.T, frame string, w, h int, label string) {
	t.Helper()
	emu := vt.NewSafeEmulator(w, h)
	if _, err := emu.WriteString(strings.ReplaceAll(frame, "\n", "\r\n")); err != nil {
		t.Fatalf("%s: emulator write: %v", label, err)
	}
	bad := 0
	var samples []string
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cell := emu.CellAt(x, y)
			if cell == nil || cell.Style.Bg == nil {
				bad++
				if len(samples) < 12 {
					ch := " "
					if cell != nil && cell.Content != "" {
						ch = cell.Content
					}
					samples = append(samples, fmt.Sprintf("(%d,%d)%q", x, y, ch))
				}
			}
		}
	}
	if bad > 0 {
		t.Errorf("%s: %d cells with default background, samples: %v", label, bad, samples)
	}
}
