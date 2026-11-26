package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

// Six-row ANSI-shadow glyphs. Solid blocks carry the accent face colour while
// box-drawing strokes render the recessed 3D shadow in Surface1.
var bannerGlyphs = map[rune][]string{
	'e': {
		"███████╗",
		"██╔════╝",
		"█████╗  ",
		"██╔══╝  ",
		"███████╗",
		"╚══════╝",
	},
	'z': {
		"███████╗",
		"╚══███╔╝",
		"  ███╔╝ ",
		" ███╔╝  ",
		"███████╗",
		"╚══════╝",
	},
	'-': {
		"      ",
		"      ",
		"████╗ ",
		"╚═══╝ ",
		"      ",
		"      ",
	},
	's': {
		" ██████╗",
		"██╔════╝",
		"╚█████╗ ",
		" ╚═══██╗",
		"██████╔╝",
		"╚═════╝ ",
	},
	'h': {
		"██╗  ██╗",
		"██║  ██║",
		"███████║",
		"██╔══██║",
		"██║  ██║",
		"╚═╝  ╚═╝",
	},
}

// renderWordmark paints "ez-ssh" using the glyph table above.
func renderWordmark(text string) string {
	type coloredGlyph struct {
		rows      []string
		faceColor lipgloss.Color
	}

	glyphs := make([]coloredGlyph, 0, len(text))
	afterDash := false
	for _, ch := range text {
		rows, ok := bannerGlyphs[ch]
		if !ok {
			continue
		}
		faceColor := theme.Blue
		switch {
		case ch == '-':
			faceColor = theme.Subtext0
			afterDash = true
		case afterDash:
			faceColor = theme.Mauve
		}
		glyphs = append(glyphs, coloredGlyph{rows: rows, faceColor: faceColor})
	}
	if len(glyphs) == 0 {
		return ""
	}

	depthStyle := lipgloss.NewStyle().Foreground(theme.Surface1).Background(theme.Crust)

	var b strings.Builder
	for y := range glyphs[0].rows {
		for i, g := range glyphs {
			if i > 0 {
				b.WriteString(theme.SpaceBg(theme.Crust, 1))
			}
			faceStyle := lipgloss.NewStyle().Foreground(g.faceColor).Bold(true).Background(theme.Crust)
			for _, r := range g.rows[y] {
				switch {
				case r == '█':
					b.WriteString(faceStyle.Render("█"))
				case r == ' ':
					b.WriteString(theme.SpaceBg(theme.Crust, 1))
				default:
					b.WriteString(depthStyle.Render(string(r)))
				}
			}
		}
		if y < len(glyphs[0].rows)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// RenderBanner returns the centered startup banner, including the version.
// It is shared by the `--version` CLI output and the TUI intro splash.
func RenderBanner(version string, width int) string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" {
		version = "1.0.0"
	}
	logo := renderWordmark("ez-ssh")
	logoW := lipgloss.Width(strings.Split(logo, "\n")[0])
	if width <= 0 {
		width = logoW + 6
	}

	verBadge := lipgloss.NewStyle().
		Foreground(theme.Blue).
		Background(theme.Surface0).
		Bold(true).
		Padding(0, 1).
		Render("v" + version)

	if width < logoW+4 {
		compact := lipgloss.NewStyle().Foreground(theme.Blue).Background(theme.Crust).Bold(true).Render("EZ") +
			lipgloss.NewStyle().Foreground(theme.Subtext0).Background(theme.Crust).Render("-") +
			lipgloss.NewStyle().Foreground(theme.Mauve).Background(theme.Crust).Bold(true).Render("SSH") +
			theme.SpaceBg(theme.Crust, 1) +
			verBadge
		return centerBannerLine(compact, width)
	}

	var b strings.Builder
	for _, line := range strings.Split(logo, "\n") {
		b.WriteString(centerBannerLine(line, width) + "\n")
	}
	ruleW := minInt(logoW, width-2)
	rule := theme.CanvasDividerStyle.Render(strings.Repeat("─", ruleW))
	tagline := lipgloss.NewStyle().Foreground(theme.Subtext0).Background(theme.Crust).Render("local ssh & sftp manager")
	sep := lipgloss.NewStyle().Foreground(theme.Surface1).Background(theme.Crust).Render("  •  ")
	metaLine := tagline + sep + verBadge
	b.WriteString(centerBannerLine(rule, width) + "\n")
	b.WriteString(centerBannerLine(metaLine, width))
	return b.String()
}

func centerBannerLine(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) > width {
		s = components.Truncate(s, width)
	}
	w := lipgloss.Width(s)
	left := (width - w) / 2
	if left < 0 {
		left = 0
	}
	right := width - left - w
	if right < 0 {
		right = 0
	}
	return theme.SpaceBg(theme.Crust, left) + s + theme.SpaceBg(theme.Crust, right)
}
