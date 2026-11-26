package components

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

// ToastLevel identifies toast styling severity.
type ToastLevel int

const (
	ToastInfo ToastLevel = iota
	ToastSuccess
	ToastError
)

// ToastExpiredMsg is sent when a toast timer expires.
type ToastExpiredMsg struct {
	ID int64
}

// ToastModel renders ephemeral top-right notifications with monotonic IDs
// so newer toasts are never dismissed early by older timers.
type ToastModel struct {
	nextID   int64
	activeID int64
	visible  bool
	level    ToastLevel
	text     string
}

// Show displays a toast notification and returns a timer command to hide it after 3s.
func (t *ToastModel) Show(level ToastLevel, text string) tea.Cmd {
	t.nextID++
	id := t.nextID
	t.activeID = id
	t.visible = true
	t.level = level
	t.text = strings.TrimSpace(text)

	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return ToastExpiredMsg{ID: id}
	})
}

// Update handles toast expiration messages.
func (t *ToastModel) Update(msg tea.Msg) {
	if exp, ok := msg.(ToastExpiredMsg); ok {
		if exp.ID == t.activeID {
			t.visible = false
		}
	}
}

// Visible reports whether a toast is currently shown.
func (t ToastModel) Visible() bool {
	return t.visible && t.text != ""
}

// View renders the formatted toast badge clipped to maxWidth.
func (t ToastModel) View(maxWidth int) string {
	if !t.Visible() || maxWidth < 6 {
		return ""
	}
	var prefix string
	var style lipgloss.Style
	switch t.level {
	case ToastSuccess:
		prefix = "✓ "
		style = theme.ToastSuccess
	case ToastError:
		prefix = "✖ "
		style = theme.ToastError
	default:
		prefix = "● "
		style = theme.ToastInfo
	}

	maxText := maxWidth - 4
	if maxText < 2 {
		maxText = 2
	}
	msg := Truncate(prefix+t.text, maxText)
	return style.Render(msg)
}

// OverlayTopRight places overlay in the top-right corner of canvas (on line 0)
// while preserving totalWidth and left-side ANSI styles.
func OverlayTopRight(canvas, overlay string, totalWidth int) string {
	if overlay == "" || totalWidth <= 0 {
		return canvas
	}
	ow := lipgloss.Width(overlay)
	if ow >= totalWidth {
		return canvas
	}

	lines := strings.Split(canvas, "\n")
	if len(lines) == 0 {
		return canvas
	}

	rowIdx := 0
	if len(lines) > 1 {
		rowIdx = 1 // Keep top border clean; float inside top-right workspace row.
	}
	leftWidth := totalWidth - ow - 1
	if leftWidth < 0 {
		leftWidth = 0
	}
	left := ansi.Cut(lines[rowIdx], 0, leftWidth)
	lw := lipgloss.Width(left)
	if lw < leftWidth {
		left += theme.SpaceBg(theme.Crust, leftWidth-lw)
	}
	lines[rowIdx] = left + overlay + theme.SpaceBg(theme.Crust, 1)
	return strings.Join(lines, "\n")
}
