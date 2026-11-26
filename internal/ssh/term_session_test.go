package ssh

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
)

func TestTermSessionPasteAndTextSelection(t *testing.T) {
	tmp := t.TempDir()
	vault, err := crypto.NewVault(filepath.Join(tmp, ".vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	srv := startTestSSHServer(t, nil)

	host := db.Host{
		ID: 9, Name: "paste-box", Hostname: srv.host, Port: srv.port, Username: "testuser",
	}
	ts, err := StartTermSession(context.Background(), host, nil, vault,
		filepath.Join(tmp, "known_hosts"), filepath.Join(tmp, "logs"), 80, 20, "secret-pw", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	waitFrame := func(substr string) string {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			frame, _, _ := ts.Render(0, true)
			if strings.Contains(frame, substr) {
				return frame
			}
			time.Sleep(15 * time.Millisecond)
		}
		frame, _, _ := ts.Render(0, true)
		t.Fatalf("timed out waiting for %q; frame:\n%s", substr, frame)
		return ""
	}
	waitFrame("READY")

	// A bracketed paste arrives as one KeyMsg with Paste=true; it must reach
	// the remote shell exactly like typed text.
	ts.WriteKey(tea.KeyMsg{Type: tea.KeyRunes, Paste: true, Runes: []rune("pasted-text")})
	ts.WriteKey(tea.KeyMsg{Type: tea.KeyEnter})
	waitFrame("echo:pasted-text")

	// A paste injected from the system clipboard must work too.
	ts.PasteText("clipboard-text")
	ts.WriteKey(tea.KeyMsg{Type: tea.KeyEnter})
	waitFrame("echo:clipboard-text")

	// Selecting the echoed text returns its plain content, and highlighting is
	// reflected in the rendered frame.
	plain, _, _ := ts.Render(0, false)
	plain = strings.ReplaceAll(plain, "\r\n", "\n")
	row := -1
	for i, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "echo:pasted-text") {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatalf("echo row not found in frame:\n%s", plain)
	}
	sel := ScreenSelection{Active: true, X0: 0, Y0: row, X1: len("echo:pasted-text") - 1, Y1: row}
	selected := ts.SelectedText(sel)
	if strings.TrimSpace(selected) != "echo:pasted-text" {
		t.Fatalf("SelectedText = %q, want %q", selected, "echo:pasted-text")
	}
	plainFrame, _, _ := ts.Render(0, false)
	selFrame, _, _ := ts.RenderWithSelection(0, false, sel)
	if plainFrame == selFrame {
		t.Fatal("expected selection highlight to change the rendered frame")
	}
}

func TestTermSessionLifecycleScrollbackAndNoLeak(t *testing.T) {
	tmp := t.TempDir()
	vault, err := crypto.NewVault(filepath.Join(tmp, ".vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	khPath := filepath.Join(tmp, "known_hosts")
	logsDir := filepath.Join(tmp, "logs")

	srv := startTestSSHServer(t, nil)

	// 1. Test wrong password error classification
	badEnc, _ := vault.Encrypt("wrong-pw")
	badHost := db.Host{
		ID: 1, Name: "bad-box", Hostname: srv.host, Port: srv.port, Username: "testuser",
		InlineSecretEnc: badEnc,
	}
	_, err = StartTermSession(context.Background(), badHost, nil, vault, khPath, logsDir, 80, 24, "", nil)
	if err == nil {
		t.Fatal("expected auth failure")
	}
	isAuth, _ := ClassifySSHError(err)
	if !isAuth {
		t.Fatalf("expected ClassifySSHError to detect auth error, got false for %v", err)
	}

	// Unreachable port error classification
	downHost := db.Host{ID: 2, Name: "down-box", Hostname: "127.0.0.1", Port: 1, Username: "testuser", InlineSecretEnc: badEnc}
	_, downErr := StartTermSession(context.Background(), downHost, nil, vault, khPath, logsDir, 80, 24, "", nil)
	if downErr == nil {
		t.Fatal("expected connection refused error")
	}
	isAuthDown, _ := ClassifySSHError(downErr)
	if isAuthDown {
		t.Fatalf("connection refused classified as auth error: %v", downErr)
	}

	// 2. Connect with overrideSecret + RecordSession
	runtime.GC()
	baseG := runtime.NumGoroutine()

	var steps []string
	goodHost := db.Host{
		ID: 3, Name: "ok-box", Hostname: srv.host, Port: srv.port, Username: "testuser",
		RecordSession: true,
	}
	ts, err := StartTermSession(context.Background(), goodHost, nil, vault, khPath, logsDir, 80, 20, "secret-pw", func(s string) {
		steps = append(steps, s)
	})
	if err != nil {
		t.Fatalf("StartTermSession error = %v", err)
	}
	if len(steps) < 3 {
		t.Fatalf("expected handshake steps, got %v", steps)
	}

	// Wait for READY greeting
	waitFrame := func(substr string) string {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			frame, _, _ := ts.Render(0, true)
			if strings.Contains(frame, substr) {
				return frame
			}
			time.Sleep(15 * time.Millisecond)
		}
		frame, _, _ := ts.Render(0, true)
		t.Fatalf("timed out waiting for %q; frame:\n%s", substr, frame)
		return ""
	}
	waitFrame("READY")

	// Send text + Enter
	ts.WriteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	ts.WriteKey(tea.KeyMsg{Type: tea.KeyEnter})
	waitFrame("echo:hello")

	// Resize PTY
	ts.Resize(100, 25)

	// Flood 2500 lines to verify scrollback cap at MaxScrollbackLines (2000)
	ts.WriteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("spam")})
	ts.WriteKey(tea.KeyMsg{Type: tea.KeyEnter})
	waitFrame("line-2499")

	_, _, sbLen := ts.Render(0, false)
	if sbLen > MaxScrollbackLines {
		t.Fatalf("scrollback len = %d, exceeds MaxScrollbackLines %d", sbLen, MaxScrollbackLines)
	}
	if sbLen == 0 {
		t.Fatal("expected non-zero scrollback lines after 2500 lines")
	}

	// Test scrollback view rendering
	histFrame, clamped, _ := ts.Render(10, true)
	if clamped != 10 || histFrame == "" {
		t.Fatalf("scrollback render failed: clamped=%d len=%d", clamped, len(histFrame))
	}

	// Close & check zero leaked goroutines
	tPath := ts.TranscriptPath()
	if tPath == "" {
		t.Fatal("expected transcript path")
	}
	if err := ts.Close(); err != nil {
		t.Fatalf("ts.Close error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= baseG+2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if cur := runtime.NumGoroutine(); cur > baseG+2 {
		t.Fatalf("goroutine leak after TermSession.Close: before=%d after=%d", baseG, cur)
	}
	_ = errors.New("")
}
