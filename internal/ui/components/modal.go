package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

// RenderModal renders a centered modal box over a Crust backdrop,
// guaranteeing total output width <= w, height <= h, and keeping title/footer intact.
func RenderModal(w, h int, title, body, footer string, maxW int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	if maxW <= 0 {
		maxW = 64
	}

	outerW := maxW
	if outerW > w-2 {
		outerW = w - 2
	}
	if outerW < 16 {
		outerW = w
	}

	// ModalBox has border (2) + horizontal padding (4).
	innerStyleW := outerW - 2
	if innerStyleW < 8 {
		innerStyleW = outerW
	}
	contentW := innerStyleW - 4
	if contentW < 4 {
		contentW = innerStyleW
	}

	maxInnerLines := h - 4 // border (2) + vertical padding (2)
	if maxInnerLines < 1 {
		maxInnerLines = 1
	}

	var titleLine string
	titleH := 0
	if title != "" {
		titleLine = padModalLine(theme.ModalTitle.Render(Truncate(title, contentW)), contentW)
		titleH = 2 // title + blank separator line
	}

	var footerLines []string
	footerH := 0
	if footer != "" {
		for _, fl := range strings.Split(footer, "\n") {
			footerLines = append(footerLines, padModalLine(fl, contentW))
		}
		footerH = len(footerLines) + 1 // blank separator + footer lines
	}

	availBody := maxInnerLines - titleH - footerH
	if availBody < 1 {
		availBody = 1
	}

	var bodyLines []string
	if body != "" {
		rawBody := strings.Split(body, "\n")
		if len(rawBody) > availBody {
			rawBody = rawBody[:availBody]
		}
		for _, bl := range rawBody {
			bodyLines = append(bodyLines, padModalLine(bl, contentW))
		}
	}

	var sections []string
	if titleLine != "" {
		sections = append(sections, titleLine)
	}
	if len(bodyLines) > 0 {
		sections = append(sections, strings.Join(bodyLines, "\n"))
	}
	if len(footerLines) > 0 {
		sections = append(sections, strings.Join(footerLines, "\n"))
	}

	inner := strings.Join(sections, "\n"+theme.SpaceBg(theme.Base, contentW)+"\n")
	allLines := strings.Split(inner, "\n")
	if len(allLines) > maxInnerLines {
		allLines = allLines[:maxInnerLines]
		inner = strings.Join(allLines, "\n")
	}

	box := theme.ModalBox.
		Width(innerStyleW).
		MaxWidth(w).
		MaxHeight(h).
		Render(inner)

	return lipgloss.Place(
		w, h,
		lipgloss.Center, lipgloss.Center,
		box,
		lipgloss.WithWhitespaceBackground(theme.Crust),
	)
}

func padModalLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if s == "" {
		return theme.SpaceBg(theme.Base, w)
	}
	t := Truncate(s, w)
	if !strings.Contains(t, "\x1b[") {
		styled := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Render(t)
		if rem := w - lipgloss.Width(t); rem > 0 {
			return styled + theme.SpaceBg(theme.Base, rem)
		}
		return styled
	}
	sample := theme.SpaceBg(theme.Base, 1)
	if strings.HasPrefix(sample, "\x1b[") && strings.HasSuffix(sample, " \x1b[0m") {
		bgOn := strings.TrimSuffix(sample, " \x1b[0m")
		t = bgOn + strings.ReplaceAll(t, "\x1b[0m", "\x1b[0m"+bgOn)
		if rem := w - lipgloss.Width(t); rem > 0 {
			t += theme.SpaceBg(theme.Base, rem)
		}
		return t + "\x1b[0m"
	}
	if rem := w - lipgloss.Width(t); rem > 0 {
		t += theme.SpaceBg(theme.Base, rem)
	}
	return t
}
