package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

// WinterTickMsg advances the gentle falling snow animation in the sidebar widget.
type WinterTickMsg struct {
	Gen int64
}

type snowflake struct {
	col   int
	row   int
	glyph rune
	color lipgloss.Color
	speed int
	drift int
}

// WinterWidget renders a cozy animated winter/Christmas TUI box inside the lower-left sidebar.
type WinterWidget struct {
	tick   int
	flakes []snowflake
}

// NewWinterWidget initializes deterministic snowflake particles for zero-allocation stepping.
func NewWinterWidget() WinterWidget {
	return WinterWidget{
		flakes: []snowflake{
			{col: 1, row: 0, glyph: '❄', color: theme.Sky, speed: 1, drift: 0},
			{col: 5, row: 2, glyph: '·', color: theme.Overlay1, speed: 1, drift: 1},
			{col: 9, row: 1, glyph: '*', color: theme.Text, speed: 1, drift: 0},
			{col: 13, row: 3, glyph: '•', color: theme.Sky, speed: 2, drift: -1},
			{col: 16, row: 0, glyph: '❄', color: theme.Text, speed: 1, drift: 0},
			{col: 3, row: 4, glyph: '·', color: theme.Overlay1, speed: 1, drift: 1},
			{col: 11, row: 2, glyph: '•', color: theme.Text, speed: 2, drift: 0},
			{col: 7, row: 0, glyph: '*', color: theme.Sky, speed: 1, drift: -1},
		},
	}
}

// Step advances snowflakes one gentle frame.
func (w *WinterWidget) Step() {
	if len(w.flakes) == 0 {
		*w = NewWinterWidget()
	}
	w.tick++
	for i := range w.flakes {
		f := &w.flakes[i]
		if w.tick%f.speed == 0 {
			f.row++
			if w.tick%3 == 0 && f.drift != 0 {
				f.col += f.drift
			}
		}
	}
}

// View renders the winter widget inside a rounded Base (#1e1e2e) box of outerW x outerH.
func (w WinterWidget) View(outerW, outerH int) string {
	if outerW < 10 || outerH < 6 {
		return ""
	}
	if len(w.flakes) == 0 {
		w = NewWinterWidget()
	}

	innerW := outerW - 2
	innerH := outerH - 2
	if innerW < 8 {
		innerW = 8
	}
	if innerH < 4 {
		innerH = 4
	}

	baseBg := lipgloss.NewStyle().Background(theme.Base)
	lines := make([]string, 0, innerH)

	// 1. Top festive banner
	banner := baseBg.Foreground(theme.Sky).Render("* ❄ ") +
		baseBg.Foreground(theme.Mauve).Bold(true).Render("EZ-SSH") +
		baseBg.Foreground(theme.Sky).Render(" ❄ *")
	lines = append(lines, padBgLine(centerLine(banner, innerW), innerW, theme.Base))

	// Scene uses up to 5 rows at the bottom if innerH >= 8, else compact 3 rows.
	sceneRows := w.buildScene(innerW, innerH)
	skyH := innerH - 1 - len(sceneRows)
	if skyH < 0 {
		skyH = 0
	}

	// 2. Falling snow rows
	for r := 0; r < skyH; r++ {
		rowParticles := make(map[int]snowflake)
		for _, f := range w.flakes {
			fy := ((f.row % maxIntW(1, skyH)) + maxIntW(1, skyH)) % maxIntW(1, skyH)
			if fy == r {
				fx := ((f.col % innerW) + innerW) % innerW
				rowParticles[fx] = f
			}
		}
		var sb strings.Builder
		runStart := 0
		for c := 0; c < innerW; c++ {
			if fl, ok := rowParticles[c]; ok {
				if c > runStart {
					sb.WriteString(theme.SpaceBg(theme.Base, c-runStart))
				}
				sb.WriteString(baseBg.Foreground(fl.color).Render(string(fl.glyph)))
				runStart = c + 1
			}
		}
		if runStart < innerW {
			sb.WriteString(theme.SpaceBg(theme.Base, innerW-runStart))
		}
		lines = append(lines, sb.String())
	}

	// 3. Snowman + Pine tree scene
	lines = append(lines, sceneRows...)
	for len(lines) < innerH {
		lines = append(lines, theme.SpaceBg(theme.Base, innerW))
	}
	if len(lines) > innerH {
		lines = lines[:innerH]
	}

	return theme.WinterBoxStyle.
		Width(innerW).
		Height(innerH).
		MaxWidth(outerW).
		MaxHeight(outerH).
		Render(strings.Join(lines, "\n"))
}

func (w WinterWidget) buildScene(innerW, innerH int) []string {
	b := lipgloss.NewStyle().Background(theme.Base)
	fg := func(c lipgloss.Color, bold bool, s string) string {
		st := b.Foreground(c)
		if bold {
			st = st.Bold(true)
		}
		return st.Render(s)
	}
	sp := func(n int) string { return theme.SpaceBg(theme.Base, n) }

	green := theme.Green
	red := theme.Red
	blue := theme.Blue
	peach := theme.Peach
	text := theme.Text
	mauve := theme.Mauve
	sky := theme.Sky

	// Strictly fixed-width 18-cell scene. Every segment (including spaces)
	// carries the widget's #1e1e2e Base background explicitly.
	raw := []string{
		sp(4) + fg(theme.Yellow, true, "★") + sp(13),
		sp(3) + fg(green, false, "/|\\") + sp(6) + fg(mauve, true, "_") + sp(5),
		sp(2) + fg(green, false, "/*") + fg(red, false, "o") + fg(green, false, "*\\") +
			sp(3) + fg(mauve, true, "_|_|_") + sp(3),
		sp(1) + fg(green, false, "/") + fg(red, false, "o") + fg(green, false, "*") +
			fg(blue, false, "o") + fg(green, false, "*") + fg(red, false, "o") + fg(green, false, "\\") +
			sp(2) + fg(text, false, "(o") + fg(peach, false, ">") + fg(text, false, "o)") + sp(3),
		fg(green, false, "/___") + fg(peach, false, "┬") + fg(green, false, "___\\") +
			sp(1) + fg(red, false, "-(═)-") + sp(3),
		fg(sky, false, "▄▄▄▄") + fg(peach, false, "┴") + fg(sky, false, "▄▄▄▄") +
			sp(1) + fg(text, false, "( : )") + sp(1) + fg(sky, false, "▄▄"),
	}

	if innerH < len(raw)+1 {
		keep := maxIntW(1, innerH-1)
		raw = raw[len(raw)-keep:]
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		out = append(out, padBgLine(centerLine(r, innerW), innerW, theme.Base))
	}
	return out
}

func centerLine(s string, width int) string {
	sw := lipgloss.Width(s)
	if sw >= width {
		return Truncate(s, width)
	}
	left := (width - sw) / 2
	right := width - sw - left
	return theme.SpaceBg(theme.Base, left) + s + theme.SpaceBg(theme.Base, right)
}

func padBgLine(s string, width int, bg lipgloss.Color) string {
	t := Truncate(s, width)
	w := lipgloss.Width(t)
	if w < width {
		return t + theme.SpaceBg(bg, width-w)
	}
	return t
}

func maxIntW(a, b int) int {
	if a > b {
		return a
	}
	return b
}
