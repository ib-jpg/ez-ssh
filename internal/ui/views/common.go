package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ssh"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

// WorkspaceHeaderHeight is the height in rows of the top rounded search/action
// bar rendered above the table box in secondary Vault modules.
const WorkspaceHeaderHeight = 3

// TableTopOffset is the Y row offset (relative to ViewMain) where table data
// rows begin: rows 0..2 = header bar, row 3 = table top border,
// row 4 = table column header, row 5 = separator line, row 6+ = data rows.
const TableTopOffset = 6

// Cross-view Bubble Tea messages dispatched to the root AppModel.
type (
	ToastMsg struct {
		Level components.ToastLevel
		Text  string
	}

	DataChangedMsg struct{}

	ExecSSHMsg struct {
		Host     db.Host
		Keychain *db.KeychainItem
	}

	OpenSessionMsg struct {
		Host     db.Host
		Keychain *db.KeychainItem
	}

	OpenSFTPMsg struct {
		Host     db.Host
		Keychain *db.KeychainItem
	}

	ExecCopyIDMsg struct {
		PubKeyPath   string
		Host         db.Host
		HostKeychain *db.KeychainItem
	}

	TunnelEventMsg struct {
		Event ssh.TunnelEvent
	}

	SnippetExecResultMsg struct {
		SnippetName string
		Host        db.Host
		Output      string
		Duration    time.Duration
		Err         error
	}

	KeyGenResultMsg struct {
		Item     db.KeychainItem
		PrivPath string
		Err      error
	}

	ToggleInspectorMsg struct{}
)

// MouseHandler is optionally implemented by views supporting left/right mouse buttons.
type MouseHandler interface {
	HandleMouse(innerX, innerY int, button tea.MouseButton, doubleClick bool) tea.Cmd
}

// ViewState represents common view capabilities implemented by all 6 modules.
type View interface {
	Init() tea.Cmd
	Reload() error
	SetSize(w, h int)
	IsModalOpen() bool
	IsSearching() bool
	Update(msg tea.Msg) tea.Cmd
	HandleClick(innerX, innerY int, doubleClick bool) tea.Cmd
	ViewMain(w, h int) string
	ViewInspector(w, h int) string
	ViewModal(fullW, fullH int) string
	Hints() []components.KeyHint
}

// Column configures a table column layout.
type Column struct {
	Title    string
	Weight   int
	MinWidth int
	MaxWidth int
}

// HeaderLayout stores hit-test X intervals for clickable header pills.
type HeaderLayout struct {
	SearchX0 int
	SearchX1 int
	NewX0    int
	NewX1    int
}

// RenderWorkspaceHeader renders a single unified 3-row rounded search bar (with
// an optional "+ New" primary pill integrated inside its right edge) on Layer 0
// Crust and returns hitboxes relative to (0,0).
func RenderWorkspaceHeader(
	width int,
	searching bool,
	searchInput textinput.Model,
	searchQuery string,
	newLabel string,
	filterBadge string,
) (string, HeaderLayout) {
	if width <= 0 {
		return "", HeaderLayout{}
	}

	contentW := maxInt(1, width-4) // border(2) + padding(2)
	icon := lipgloss.NewStyle().Foreground(theme.Blue).Background(theme.Base).Render("󰍉 ")
	iconW := lipgloss.Width(icon)

	var newPill string
	showNewPill := false
	if newLabel != "" {
		candidate := theme.ActionBarPrimaryPill.Render("+ " + newLabel)
		if contentW >= lipgloss.Width(candidate)+6 {
			newPill = candidate
			showNewPill = true
		}
	}

	hasQuery := !searching && strings.TrimSpace(searchQuery) != ""
	var rightPart string
	if showNewPill {
		if hasQuery && contentW >= lipgloss.Width(newPill)+14 {
			clearGlyph := lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base).Render("×")
			rightPart = clearGlyph + theme.SpaceBg(theme.Base, 1) + newPill
		} else {
			rightPart = newPill
		}
	} else {
		if filterBadge != "" && contentW >= 20 {
			rightPart = theme.TagPill.Render(components.Truncate(filterBadge, maxInt(6, contentW/3)))
		} else if hasQuery && contentW >= 14 {
			rightPart = lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base).Render("×")
		}
	}
	rightW := lipgloss.Width(rightPart)

	rightGap := 0
	if rightW > 0 {
		rightGap = 1
	}
	inputAvail := maxInt(1, contentW-iconW-rightW-rightGap)

	var leftPart string
	if searching {
		leftPart = icon + RenderCleanTextInput(&searchInput, inputAvail, theme.Base)
	} else if strings.TrimSpace(searchQuery) != "" {
		qText := components.Truncate(searchQuery, inputAvail)
		leftPart = icon + lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Render(qText)
	} else {
		phText := strings.TrimSpace(searchInput.Placeholder)
		if phText == "" {
			phText = "Search..."
		}
		ph := components.Truncate(phText, inputAvail)
		leftPart = icon + lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base).Render(ph)
	}

	lw := lipgloss.Width(leftPart)
	var barContent string
	if rightW > 0 {
		gap := maxInt(1, contentW-lw-rightW)
		if lw+gap+rightW > contentW {
			leftPart = components.Truncate(leftPart, maxInt(1, contentW-rightW-1))
			gap = maxInt(1, contentW-lipgloss.Width(leftPart)-rightW)
		}
		barContent = leftPart + theme.SpaceBg(theme.Base, gap) + rightPart
	} else {
		gap := maxInt(0, contentW-lw)
		barContent = leftPart + theme.SpaceBg(theme.Base, gap)
	}
	barContent = components.Truncate(barContent, contentW)
	if cw := lipgloss.Width(barContent); cw < contentW {
		barContent += theme.SpaceBg(theme.Base, contentW-cw)
	}

	barStyle := theme.SearchBarBlurred
	if searching {
		barStyle = theme.SearchBarFocused
	}
	searchBox := barStyle.Width(maxInt(2, width-2)).MaxWidth(width).MaxHeight(3).Render(barContent)

	sLines := strings.Split(searchBox, "\n")
	rows := make([]string, WorkspaceHeaderHeight)
	for r := 0; r < WorkspaceHeaderHeight; r++ {
		var row string
		if r < len(sLines) {
			row = sLines[r]
		}
		rows[r] = padBgLine(row, width, theme.Crust)
	}

	if showNewPill {
		pillW := lipgloss.Width(newPill)
		pillStartX := maxInt(0, width-2-pillW)
		return strings.Join(rows, "\n"), HeaderLayout{
			SearchX0: 0,
			SearchX1: pillStartX,
			NewX0:    pillStartX,
			NewX1:    width,
		}
	}

	return strings.Join(rows, "\n"), HeaderLayout{
		SearchX0: 0,
		SearchX1: width,
		NewX0:    -1,
		NewX1:    -1,
	}
}

// TablePaneInnerSize returns the inner width and height available to RenderTable
// below the 3-row workspace header and inside the rounded table container box.
func TablePaneInnerSize(w, h int) (int, int) {
	tableOuterH := maxInt(3, h-WorkspaceHeaderHeight)
	return maxInt(6, w-4), maxInt(1, tableOuterH-2)
}

// RenderWorkspacePane combines the 3-row top search/action header with the
// rounded table container box inside w x h.
func RenderWorkspacePane(w, h int, hdr string, searching bool, tableContent string) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	tableOuterH := h - WorkspaceHeaderHeight
	if tableOuterH < 3 {
		hdrLines := strings.Split(hdr, "\n")
		out := make([]string, 0, h)
		for i := 0; i < h; i++ {
			if i < len(hdrLines) {
				out = append(out, padBgLine(hdrLines[i], w, theme.Crust))
			} else {
				out = append(out, theme.SpaceBg(theme.Crust, w))
			}
		}
		return strings.Join(out, "\n")
	}

	boxStyle := theme.BorderActive
	if searching {
		boxStyle = boxStyle.BorderForeground(theme.Surface0)
	}
	tableBox := boxStyle.
		Padding(0, 1).
		Width(maxInt(2, w-2)).
		Height(maxInt(1, tableOuterH-2)).
		MaxWidth(w).
		MaxHeight(tableOuterH).
		Render(tableContent)

	return hdr + "\n" + tableBox
}

// ComputeColumnWidths distributes totalWidth across cols while reserving 2 cells for cursor.
func ComputeColumnWidths(totalWidth int, cols []Column) []int {
	widths := make([]int, len(cols))
	if len(cols) == 0 || totalWidth <= 2 {
		return widths
	}

	// 2 cells for leading "▸ " or "  ", plus 1 space between columns.
	avail := totalWidth - 2 - (len(cols) - 1)
	if avail < len(cols) {
		for i := range cols {
			widths[i] = 1
		}
		return widths
	}

	used := 0
	totalWeight := 0
	for i, c := range cols {
		w := c.MinWidth
		if w <= 0 {
			w = 4
		}
		widths[i] = w
		used += w
		if c.Weight > 0 {
			totalWeight += c.Weight
		} else {
			totalWeight++
		}
	}

	// Shrink proportionally if sum of MinWidths exceeds avail.
	if used > avail {
		excess := used - avail
		for i := len(cols) - 1; i >= 0 && excess > 0; i-- {
			canDrop := widths[i] - 2
			if canDrop > 0 {
				drop := minInt(canDrop, excess)
				widths[i] -= drop
				excess -= drop
			}
		}
		return widths
	}

	extra := avail - used
	if extra > 0 && totalWeight > 0 {
		for i, c := range cols {
			weight := c.Weight
			if weight <= 0 {
				weight = 1
			}
			add := (extra * weight) / totalWeight
			if c.MaxWidth > 0 && widths[i]+add > c.MaxWidth {
				add = maxInt(0, c.MaxWidth-widths[i])
			}
			widths[i] += add
		}
		// Give remaining rounding slack to flexible columns.
		sum := 0
		for _, w := range widths {
			sum += w
		}
		rem := avail - sum
		for i := 0; i < len(cols) && rem > 0; i++ {
			if cols[i].MaxWidth == 0 || widths[i] < cols[i].MaxWidth {
				widths[i]++
				rem--
			}
		}
	}
	return widths
}

// RenderTable renders a scrollable header + separator + fixed-height data rows.
func RenderTable(
	width, height int,
	cols []Column,
	rows [][]string,
	cursor, offset int,
	emptyMsg string,
) (string, int) {
	return RenderTableStyled(width, height, cols, rows, cursor, offset, emptyMsg, nil)
}

// RenderTableStyled renders a scrollable table with an optional per-cell styler
// invoked on unselected rows (selected rows use uniform highlight background).
func RenderTableStyled(
	width, height int,
	cols []Column,
	rows [][]string,
	cursor, offset int,
	emptyMsg string,
	cellStyler func(rowIdx, colIdx int, paddedCell string) string,
) (string, int) {
	if width <= 0 || height <= 0 {
		return "", 0
	}

	colWidths := ComputeColumnWidths(width, cols)

	// Build column header row.
	var hdrB strings.Builder
	hdrB.WriteString("  ")
	for i, c := range cols {
		if i > 0 {
			hdrB.WriteByte(' ')
		}
		hdrB.WriteString(components.PadRight(strings.ToUpper(c.Title), colWidths[i]))
	}
	hdrLine := theme.TableHeader.Render(components.PadRight(hdrB.String(), width))
	sepLine := theme.TableSeparator.Render(strings.Repeat("─", width))

	dataRows := maxInt(1, height-2)
	if len(rows) == 0 {
		lines := make([]string, 0, height)
		lines = append(lines, hdrLine)
		if height > 1 {
			lines = append(lines, sepLine)
		}
		for i := 0; i < dataRows && len(lines) < height; i++ {
			if i == 0 {
				msg := theme.RowMuted.Render("  " + components.Truncate(emptyMsg, maxInt(1, width-2)))
				lines = append(lines, padBgLine(msg, width, theme.Base))
			} else {
				lines = append(lines, theme.SpaceBg(theme.Base, width))
			}
		}
		return strings.Join(lines, "\n"), 0
	}

	cursor = clampInt(cursor, 0, len(rows)-1)
	offset = EnsureVisible(cursor, offset, dataRows, len(rows))

	lines := make([]string, 0, height)
	lines = append(lines, hdrLine)
	if height > 1 {
		lines = append(lines, sepLine)
	}

	for r := 0; r < dataRows && len(lines) < height; r++ {
		idx := offset + r
		if idx >= len(rows) {
			lines = append(lines, theme.SpaceBg(theme.Base, width))
			continue
		}
		row := rows[idx]
		if idx == cursor {
			var lineB strings.Builder
			lineB.WriteString("▸ ")
			for c := 0; c < len(cols); c++ {
				if c > 0 {
					lineB.WriteByte(' ')
				}
				val := ""
				if c < len(row) {
					val = row[c]
				}
				lineB.WriteString(components.PadRight(val, colWidths[c]))
			}
			lines = append(lines, theme.RowSelected.Render(components.PadRight(lineB.String(), width)))
			continue
		}

		var lineB strings.Builder
		lineB.WriteString(theme.RowNormal.Render("  "))
		usedW := 2
		for c := 0; c < len(cols); c++ {
			if c > 0 {
				lineB.WriteString(theme.SpaceBg(theme.Base, 1))
				usedW++
			}
			val := ""
			if c < len(row) {
				val = row[c]
			}
			cell := components.PadRight(val, colWidths[c])
			usedW += colWidths[c]
			if cellStyler != nil {
				lineB.WriteString(cellStyler(idx, c, cell))
			} else {
				lineB.WriteString(theme.RowNormal.Render(cell))
			}
		}
		if usedW < width {
			lineB.WriteString(theme.SpaceBg(theme.Base, width-usedW))
		}
		lines = append(lines, lineB.String())
	}

	return strings.Join(lines, "\n"), offset
}

// EnsureVisible adjusts viewport offset so cursor stays within [offset, offset+visible-1].
func EnsureVisible(cursor, offset, visible, total int) int {
	if visible <= 0 || total <= 0 {
		return 0
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		cursor = total - 1
	}
	if offset < 0 {
		offset = 0
	}
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+visible {
		offset = cursor - visible + 1
	}
	maxOff := maxInt(0, total-visible)
	if offset > maxOff {
		offset = maxOff
	}
	return offset
}

// RenderInspectorLines joins key-value lines safely clipped to w x h.
func RenderInspectorLines(w, h int, title string, lines []string) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	out := make([]string, 0, h)
	if title != "" {
		out = append(out, padBgLine(theme.InspectorTitle.Render(components.Truncate(title, w)), w, theme.Mantle))
	}
	for _, l := range lines {
		if len(out) >= h {
			break
		}
		out = append(out, padBgLine(l, w, theme.Mantle))
	}
	for len(out) < h {
		out = append(out, theme.SpaceBg(theme.Mantle, w))
	}
	return strings.Join(out, "\n")
}

// padBgLine clips s to width and fills any remaining cells with an explicit
// background so ANSI resets never expose the terminal's default background.
func padBgLine(s string, width int, bg lipgloss.Color) string {
	s = components.Truncate(s, width)
	if w := lipgloss.Width(s); w < width {
		s += theme.SpaceBg(bg, width-w)
	}
	return s
}

// FormatKV renders a labeled inspector row safely.
func FormatKV(label, value string, width int) string {
	lbl := theme.InspectorLabel.Render(label + ": ")
	rem := maxInt(1, width-lipgloss.Width(lbl))
	val := theme.InspectorValue.Render(components.Truncate(value, rem))
	return lbl + val
}

// RenderPickerList renders a scrollable single-select list inside modals.
func RenderPickerList(items []string, cursor, maxVisible, rowWidth int) string {
	if len(items) == 0 {
		return theme.RowMuted.Render("  (no items)")
	}
	if maxVisible < 3 {
		maxVisible = 3
	}
	cursor = clampInt(cursor, 0, len(items)-1)
	start := EnsureVisible(cursor, 0, maxVisible, len(items))
	end := minInt(len(items), start+maxVisible)

	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		if i == cursor {
			rows = append(rows, theme.RowSelected.Render(components.PadRight("▸ "+items[i], rowWidth)))
		} else {
			rows = append(rows, theme.RowNormal.Render(components.PadRight("  "+items[i], rowWidth)))
		}
	}
	return strings.Join(rows, "\n")
}

// NewStyledInput creates a Catppuccin-styled textinput.Model with explicit Base background.
func NewStyledInput(placeholder string, charLimit int) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = charLimit
	ti.Prompt = ""
	ti.PromptStyle = lipgloss.NewStyle().Background(theme.Base)
	ti.TextStyle = lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base)
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(theme.Blue).Background(theme.Base)
	ti.Cursor.TextStyle = lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base)
	return ti
}

// RenderCleanTextInput renders a single-line textinput padded to exact width with
// uniform bg so bubbles/textinput's raw placeholder trailing spaces never leak dark bars.
func RenderCleanTextInput(ti *textinput.Model, width int, bg lipgloss.Color) string {
	if width <= 0 || ti == nil {
		return ""
	}
	ti.Prompt = ""
	ti.PromptStyle = lipgloss.NewStyle().Background(bg)
	ti.TextStyle = lipgloss.NewStyle().Foreground(theme.Text).Background(bg)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(theme.Overlay1).Background(bg)
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(theme.Blue).Background(bg)
	ti.Cursor.TextStyle = lipgloss.NewStyle().Foreground(theme.Text).Background(bg)

	// bubbles/textinput appends 1 cursor column beyond ti.Width on non-empty text.
	wantW := maxInt(1, width-1)
	if ti.Width != wantW {
		ti.Width = wantW
		ti.SetCursor(ti.Position())
	}

	raw := ti.View()
	clean := strings.TrimRight(raw, " ")
	clean = components.Truncate(clean, width)
	pad := maxInt(0, width-lipgloss.Width(clean))
	if pad > 0 {
		clean += theme.SpaceBg(bg, pad)
	}
	return clean
}

// RenderRoundedInputBox wraps a textinput inside a 3-row RoundedBorder box
// with consistent inner width and explicit surface backgrounds (no ANSI bleed).
func RenderRoundedInputBox(ti *textinput.Model, outerW int, focused bool, inputBg, parentBg lipgloss.Color) string {
	if outerW < 8 {
		outerW = 8
	}
	innerW := maxInt(2, outerW-4) // border (2) + horizontal padding (2)
	line := RenderCleanTextInput(ti, innerW, inputBg)

	borderFg := theme.Surface0
	if focused {
		borderFg = theme.Blue
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderFg).
		BorderBackground(parentBg).
		Background(inputBg).
		Foreground(theme.Text).
		Padding(0, 1).
		Width(outerW - 2).
		MaxWidth(outerW).
		Render(line)
}

// RenderRoundedValueBox wraps an already-rendered single-line selector or value
// inside the exact same 3-row RoundedBorder geometry as RenderRoundedInputBox.
func RenderRoundedValueBox(inner string, outerW int, focused bool, inputBg, parentBg lipgloss.Color) string {
	if outerW < 8 {
		outerW = 8
	}
	innerW := maxInt(2, outerW-4)
	clamped := components.Truncate(inner, innerW)
	if pad := maxInt(0, innerW-lipgloss.Width(clamped)); pad > 0 {
		clamped += theme.SpaceBg(inputBg, pad)
	}

	borderFg := theme.Surface0
	if focused {
		borderFg = theme.Blue
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderFg).
		BorderBackground(parentBg).
		Background(inputBg).
		Foreground(theme.Text).
		Padding(0, 1).
		Width(outerW - 2).
		MaxWidth(outerW).
		Render(clamped)
}

// RenderCleanMultiline normalizes each line of a rendered block (such as
// bubbles/textarea or bubbles/viewport) to exact width and guarantees every cell
// and ANSI reset transition carries bg so default gray never bleeds through.
func RenderCleanMultiline(block string, width int, bg lipgloss.Color) string {
	if width <= 0 {
		return ""
	}
	bgOn := ""
	if sample := theme.SpaceBg(bg, 1); strings.HasPrefix(sample, "\x1b[") && strings.HasSuffix(sample, " \x1b[0m") {
		bgOn = strings.TrimSuffix(sample, " \x1b[0m")
	}

	rawLines := strings.Split(block, "\n")
	out := make([]string, len(rawLines))
	for i, line := range rawLines {
		clean := strings.TrimRight(line, " ")
		if !strings.Contains(clean, "\x1b[") {
			clamped := components.Truncate(clean, width)
			rendered := lipgloss.NewStyle().Foreground(theme.Text).Background(bg).Render(clamped)
			if pad := maxInt(0, width-lipgloss.Width(clamped)); pad > 0 {
				rendered += theme.SpaceBg(bg, pad)
			}
			out[i] = rendered
			continue
		}
		clean = components.Truncate(clean, width)
		pad := maxInt(0, width-lipgloss.Width(clean))
		if bgOn != "" {
			clean = bgOn + strings.ReplaceAll(clean, "\x1b[0m", "\x1b[0m"+bgOn)
		}
		if pad > 0 {
			clean += theme.SpaceBg(bg, pad)
		}
		if bgOn != "" {
			clean += "\x1b[0m"
		}
		out[i] = clean
	}
	return strings.Join(out, "\n")
}

// HumanDuration formats seconds cleanly (e.g. "42s", "3m 12s", "1h 05m").
func HumanDuration(sec int) string {
	if sec < 0 {
		sec = 0
	}
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm %02ds", sec/60, sec%60)
	}
	return fmt.Sprintf("%dh %02dm", sec/3600, (sec%3600)/60)
}

// HumanTimeAgo formats a timestamp relative to now.
func HumanTimeAgo(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "Never"
	}
	d := time.Since(*t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "Just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d mins ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}

func clampInt(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
