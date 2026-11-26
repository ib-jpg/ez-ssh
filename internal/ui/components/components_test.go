package components

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/muesli/termenv"

	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

func TestFuzzyMatch(t *testing.T) {
	if !FuzzyMatch("", "anything") {
		t.Fatal("empty query must match")
	}
	if !FuzzyMatch("pw1", "prod-web-01") {
		t.Fatal("expected subsequence pw1 to match prod-web-01")
	}
	if !FuzzyMatch("prod 10.0", "prod-web-01", "10.0.1.12") {
		t.Fatal("expected multi-token query to match across fields")
	}
	if FuzzyMatch("db2", "prod-web-01", "10.0.1.12") {
		t.Fatal("unexpected match for db2")
	}
}

func TestTruncateAndPadRight(t *testing.T) {
	nerd := "󰒋 prod-web-01-very-long-hostname"
	out := Truncate(nerd, 12)
	if w := lipgloss.Width(out); w > 12 {
		t.Fatalf("Truncate width = %d, want <= 12 (%q)", w, out)
	}

	padded := PadRight("abc", 8)
	if w := lipgloss.Width(padded); w != 8 {
		t.Fatalf("PadRight width = %d, want 8", w)
	}
}

func TestSanitizeTranscript(t *testing.T) {
	raw := []byte("\x1b]0;title\x07\x1b[32mhello\x1b[0m\r\nworld\rspinner\x1b[?2004h\x00!\n")
	got := SanitizeTranscript(raw)
	want := "hello\nworldspinner!\n"
	if got != want {
		t.Fatalf("SanitizeTranscript = %q, want %q", got, want)
	}
}

func TestToastMonotonicExpiration(t *testing.T) {
	var toast ToastModel
	cmd1 := toast.Show(ToastInfo, "first")
	msg1 := ToastExpiredMsg{ID: 1}
	_ = cmd1

	_ = toast.Show(ToastSuccess, "second")
	toast.Update(msg1) // Older timer must not dismiss newer toast.
	if !toast.Visible() {
		t.Fatal("older timer dismissed newer toast")
	}

	toast.Update(ToastExpiredMsg{ID: 2})
	if toast.Visible() {
		t.Fatal("expected toast to hide on matching ID")
	}
}

func TestConfirmDialog(t *testing.T) {
	var c ConfirmModel
	c.Open("delete", "Delete Host", "Sure?", true)
	// Danger defaults FocusYes to false.
	confirmed, closed := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if confirmed || !closed {
		t.Fatalf("danger enter default = (%v, %v), want (false, true)", confirmed, closed)
	}

	c.Open("delete", "Delete Host", "Sure?", true)
	confirmed, closed = c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if !confirmed || !closed {
		t.Fatalf("y key = (%v, %v), want (true, true)", confirmed, closed)
	}
}

func TestRenderModalAndKeyHintsBounds(t *testing.T) {
	modal := RenderModal(60, 18, "Test Modal", "Line 1\nLine 2", "[Enter] Save", 50)
	lines := strings.Split(modal, "\n")
	if len(lines) > 18 {
		t.Fatalf("modal lines = %d, want <= 18", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 60 {
			t.Fatalf("line %d width = %d, want <= 60", i, w)
		}
	}

	hints := RenderKeyHints(45, []KeyHint{
		{Key: "Enter", Desc: "Connect"},
		{Key: "n", Desc: "New"},
		{Key: "e", Desc: "Edit"},
		{Key: "d", Desc: "Delete"},
		{Key: "q", Desc: "Quit"},
	})
	if strings.Contains(hints, "\n") {
		t.Fatalf("keyhints must be single line, got %q", hints)
	}
	if w := lipgloss.Width(hints); w > 45 {
		t.Fatalf("keyhints width = %d, want <= 45", w)
	}
}

func TestOverlayAtBoundsAndWinterWidget(t *testing.T) {
	canvasLine := theme.SpaceBg(theme.Crust, 40)
	canvas := strings.Join([]string{canvasLine, canvasLine, canvasLine, canvasLine, canvasLine}, "\n")
	box := theme.DropdownBox.Width(14).Render("Action 1\nAction 2")
	out := OverlayAt(canvas, box, 25, 2, 40, 5)
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("OverlayAt lines = %d, want 5", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 40 {
			t.Fatalf("OverlayAt line %d width = %d > 40", i, w)
		}
	}
	if !strings.Contains(out, "Action 1") {
		t.Fatal("expected overlay content present")
	}

	ww := NewWinterWidget()
	ww.Step()
	ww.Step()
	wView := ww.View(23, 12)
	wLines := strings.Split(wView, "\n")
	if len(wLines) > 12 {
		t.Fatalf("WinterWidget lines = %d > 12", len(wLines))
	}
	for i, l := range wLines {
		if w := lipgloss.Width(l); w > 23 {
			t.Fatalf("WinterWidget line %d width = %d > 23", i, w)
		}
	}
	if !strings.Contains(wView, "EZ-SSH") {
		t.Fatal("expected EZ-SSH banner in WinterWidget")
	}

	// Verify exact 18-col scene symmetry and zero default-background cells under TrueColor.
	origProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(origProfile)

	w23 := ww.View(23, 18)
	w23Plain := ansi.Strip(w23)
	wantScene := []string{
		"    ★             ",
		"   /|\\      _     ",
		"  /*o*\\   _|_|_   ",
		" /o*o*o\\  (o>o)   ",
		"/___┬___\\ -(═)-   ",
		"▄▄▄▄┴▄▄▄▄ ( : ) ▄▄",
	}
	for i, want := range wantScene {
		if gotW := lipgloss.Width(want); gotW != 18 {
			t.Fatalf("template line %d width = %d, want 18", i, gotW)
		}
		if !strings.Contains(w23Plain, want) {
			t.Fatalf("WinterWidget(23,18) missing exact aligned scene line %d (%q)\n%s", i, want, w23Plain)
		}
	}

	for _, sz := range [][2]int{{23, 18}, {19, 14}} {
		frame := ww.View(sz[0], sz[1])
		emu := vt.NewSafeEmulator(sz[0], sz[1])
		if _, err := emu.WriteString(strings.ReplaceAll(frame, "\n", "\r\n")); err != nil {
			t.Fatalf("WinterWidget(%dx%d) emulator write: %v", sz[0], sz[1], err)
		}
		for y := 0; y < sz[1]; y++ {
			for x := 0; x < sz[0]; x++ {
				cell := emu.CellAt(x, y)
				if cell == nil || cell.Style.Bg == nil {
					t.Fatalf("WinterWidget(%dx%d) cell (%d,%d) has default/nil background", sz[0], sz[1], x, y)
				}
			}
		}
	}
}
