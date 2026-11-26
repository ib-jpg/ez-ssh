package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Truncate safely shortens s to fit within maxWidth terminal cells,
// preserving UTF-8 and ANSI/Nerd Font widths without corrupting runes.
func Truncate(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return ansi.Truncate(s, 1, "")
	}
	return ansi.Truncate(s, maxWidth, "…")
}

// PadRight truncates s if wider than width, or pads with spaces on the right
// so the returned string has exact visual cell width equal to width.
func PadRight(s string, width int) string {
	if width <= 0 {
		return ""
	}
	t := Truncate(s, width)
	w := lipgloss.Width(t)
	if w < width {
		return t + strings.Repeat(" ", width-w)
	}
	return t
}
