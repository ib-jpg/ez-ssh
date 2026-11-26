package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

// KeyHint describes a single contextual shortcut in the footer bar.
type KeyHint struct {
	Key  string
	Desc string
}

// RenderKeyHints renders a single-line contextual keybinding footer bar padded to width.
func RenderKeyHints(width int, hints []KeyHint) string {
	if width <= 0 {
		return ""
	}

	var b strings.Builder
	used := 1 // leading space
	b.WriteString(theme.SpaceBg(theme.Mantle, 1))

	for _, h := range hints {
		seg := theme.KeyHintKey.Render("["+h.Key+"]") +
			theme.SpaceBg(theme.Mantle, 1) +
			theme.KeyHintDesc.Render(h.Desc) +
			theme.SpaceBg(theme.Mantle, 2)
		segW := lipgloss.Width(seg)
		if used+segW > width {
			break
		}
		b.WriteString(seg)
		used += segW
	}

	line := Truncate(b.String(), width)
	w := lipgloss.Width(line)
	if w < width {
		line += theme.SpaceBg(theme.Mantle, width-w)
	}

	return theme.FooterBar.Width(width).MaxHeight(1).Render(line)
}
