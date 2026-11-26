package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

// OverlayAt composites overlay onto canvas at cell coordinates (x, y) while
// preserving totalW x totalH bounds and surrounding ANSI styles.
func OverlayAt(canvas, overlay string, x, y, totalW, totalH int) string {
	if canvas == "" || overlay == "" || totalW <= 0 || totalH <= 0 {
		return canvas
	}

	ovLines := strings.Split(overlay, "\n")
	if len(ovLines) == 0 {
		return canvas
	}
	ovW := lipgloss.Width(overlay)
	ovH := len(ovLines)

	if x+ovW > totalW {
		x = totalW - ovW
	}
	if x < 0 {
		x = 0
	}
	if y+ovH > totalH {
		y = totalH - ovH
	}
	if y < 0 {
		y = 0
	}

	canvasLines := strings.Split(canvas, "\n")
	for len(canvasLines) < totalH {
		canvasLines = append(canvasLines, "")
	}

	for r := 0; r < ovH && y+r < len(canvasLines); r++ {
		baseLine := canvasLines[y+r]
		ovLine := Truncate(ovLines[r], totalW-x)
		ow := lipgloss.Width(ovLine)

		left := ansi.Cut(baseLine, 0, x)
		if lw := lipgloss.Width(left); lw < x {
			left += theme.SpaceBg(theme.Crust, x-lw)
		}
		right := ansi.Cut(baseLine, x+ow, totalW)
		combined := left + "\x1b[0m" + ovLine + "\x1b[0m" + right
		if lipgloss.Width(combined) > totalW {
			combined = Truncate(combined, totalW)
		}
		canvasLines[y+r] = combined
	}

	return strings.Join(canvasLines, "\n")
}
