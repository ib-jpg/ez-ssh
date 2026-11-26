package views

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ib-jpg/ez-ssh/internal/clipboard"
	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

const maxTranscriptReadBytes = 2 * 1024 * 1024 // 2 MiB memory safety cap

// LogsView implements Modul 6: SSH connection audit logs, host filter,
// old log cleanup, and lazy-loaded transcript viewer.
type LogsView struct {
	store *db.Store

	all         []db.ConnectionLog
	filtered    []db.ConnectionLog
	hostFilter  string
	hostOptions []string

	cursor int
	offset int
	width  int
	height int

	searching    bool
	searchInput  textinput.Model
	searchQuery  string
	headerLayout HeaderLayout

	confirm components.ConfirmModel

	viewerOpen     bool
	viewerLog      db.ConnectionLog
	viewerText     string
	viewerViewport viewport.Model
}

// NewLogsView constructs a LogsView backed by store.
func NewLogsView(store *db.Store) *LogsView {
	vp := viewport.New(60, 14)
	vp.MouseWheelEnabled = true
	v := &LogsView{
		store:          store,
		searchInput:    NewStyledInput("Search logs...", 64),
		viewerViewport: vp,
	}
	_ = v.Reload()
	return v
}

func (v *LogsView) Init() tea.Cmd { return nil }

func (v *LogsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

func (v *LogsView) IsModalOpen() bool {
	return v.viewerOpen || v.confirm.Active
}

func (v *LogsView) IsSearching() bool {
	return v.searching
}

func (v *LogsView) Reload() error {
	logs, err := v.store.ListLogs(v.hostFilter, 500)
	if err != nil {
		return err
	}
	hosts, err := v.store.ListDistinctLogHosts()
	if err == nil {
		v.hostOptions = hosts
	}
	v.all = logs
	v.applyFilter()
	return nil
}

func (v *LogsView) applyFilter() {
	q := v.searchQuery
	if v.searching {
		q = v.searchInput.Value()
	}
	v.filtered = v.filtered[:0]
	for _, l := range v.all {
		if components.FuzzyMatch(q, l.HostName, l.TargetAddr, l.Status) {
			v.filtered = append(v.filtered, l)
		}
	}
	if len(v.filtered) == 0 {
		v.cursor = 0
		v.offset = 0
	} else if v.cursor >= len(v.filtered) {
		v.cursor = len(v.filtered) - 1
	}
}

func (v *LogsView) selected() (db.ConnectionLog, bool) {
	if len(v.filtered) == 0 || v.cursor < 0 || v.cursor >= len(v.filtered) {
		return db.ConnectionLog{}, false
	}
	return v.filtered[v.cursor], true
}

func (v *LogsView) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.MouseMsg:
		if v.viewerOpen {
			var cmd tea.Cmd
			v.viewerViewport, cmd = v.viewerViewport.Update(m)
			return cmd
		}
		if v.IsModalOpen() {
			return nil
		}
		if m.Button == tea.MouseButtonWheelUp {
			v.moveCursor(-1)
		} else if m.Button == tea.MouseButtonWheelDown {
			v.moveCursor(1)
		}
		return nil

	case tea.KeyMsg:
		if v.confirm.Active {
			confirmed, closed := v.confirm.Update(m)
			if closed && confirmed {
				return v.executeClearLogs()
			}
			return nil
		}
		if v.viewerOpen {
			return v.updateViewerModal(m)
		}

		if v.searching {
			switch m.String() {
			case "esc":
				v.searching = false
				v.searchInput.Blur()
				v.searchInput.SetValue("")
				v.searchQuery = ""
				v.applyFilter()
				return nil
			case "enter":
				v.searchQuery = v.searchInput.Value()
				v.searching = false
				v.searchInput.Blur()
				v.applyFilter()
				return nil
			}
			var cmd tea.Cmd
			v.searchInput, cmd = v.searchInput.Update(m)
			v.searchQuery = v.searchInput.Value()
			v.applyFilter()
			return cmd
		}

		switch m.String() {
		case "esc":
			if v.searchQuery != "" {
				v.searchQuery = ""
				v.searchInput.SetValue("")
				v.applyFilter()
			} else if v.hostFilter != "" {
				v.hostFilter = ""
				_ = v.Reload()
			}
		case "/":
			v.searching = true
			v.searchInput.SetValue(v.searchQuery)
			v.searchInput.CursorEnd()
			return v.searchInput.Focus()
		case "j", "down":
			v.moveCursor(1)
		case "k", "up":
			v.moveCursor(-1)
		case "g", "home":
			v.cursor = 0
			v.offset = 0
		case "G", "end":
			if len(v.filtered) > 0 {
				v.cursor = len(v.filtered) - 1
			}
		case "enter":
			return v.openSelectedTranscript()
		case "f":
			return v.cycleHostFilter()
		case "C":
			v.confirm.Open("clear-old-logs", "Clear Old Logs", "Delete connection log entries older than 30 days?", true)
		}
	}
	return nil
}

func (v *LogsView) HandleClick(innerX, innerY int, doubleClick bool) tea.Cmd {
	if v.IsModalOpen() {
		return nil
	}
	if innerY >= 0 && innerY < WorkspaceHeaderHeight {
		if innerX >= v.headerLayout.SearchX0 && innerX < v.headerLayout.SearchX1 {
			v.searching = true
			v.searchInput.SetValue(v.searchQuery)
			v.searchInput.CursorEnd()
			return v.searchInput.Focus()
		}
		return nil
	}
	if v.searching {
		v.searching = false
		v.searchInput.Blur()
	}
	if innerY >= TableTopOffset && (v.height <= 0 || innerY < v.height-1) {
		idx := v.offset + (innerY - TableTopOffset)
		if idx >= 0 && idx < len(v.filtered) {
			v.cursor = idx
			if doubleClick {
				return v.openSelectedTranscript()
			}
		}
	}
	return nil
}

func (v *LogsView) moveCursor(delta int) {
	if len(v.filtered) == 0 {
		v.cursor = 0
		return
	}
	v.cursor = clampInt(v.cursor+delta, 0, len(v.filtered)-1)
}

func (v *LogsView) cycleHostFilter() tea.Cmd {
	if len(v.hostOptions) == 0 {
		return toastCmd(components.ToastInfo, "No host logs to filter")
	}
	if v.hostFilter == "" {
		v.hostFilter = v.hostOptions[0]
	} else {
		next := ""
		for i, h := range v.hostOptions {
			if h == v.hostFilter {
				if i+1 < len(v.hostOptions) {
					next = v.hostOptions[i+1]
				}
				break
			}
		}
		v.hostFilter = next
	}
	_ = v.Reload()
	label := "All Hosts"
	if v.hostFilter != "" {
		label = v.hostFilter
	}
	return toastCmd(components.ToastInfo, "Log filter: "+label)
}

func (v *LogsView) executeClearLogs() tea.Cmd {
	removed, err := v.store.ClearOldLogs(30)
	if err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Clear logs failed: %v", err))
	}
	_ = v.Reload()
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, fmt.Sprintf("Cleared %d log(s) older than 30 days", removed)),
	)
}

func (v *LogsView) openSelectedTranscript() tea.Cmd {
	l, ok := v.selected()
	if !ok {
		return nil
	}
	if strings.TrimSpace(l.TranscriptPath) == "" {
		return toastCmd(components.ToastInfo, "No session recording saved for this connection")
	}

	raw, truncated, err := readTranscriptTail(l.TranscriptPath, maxTranscriptReadBytes)
	if err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Open transcript: %v", err))
	}
	clean := components.SanitizeTranscript(raw)
	if truncated {
		clean = "[...earlier session output truncated to keep RAM < 25 MB...]\n\n" + clean
	}
	if strings.TrimSpace(clean) == "" {
		clean = "(session transcript is empty)"
	}

	v.viewerOpen = true
	v.viewerLog = l
	v.viewerText = clean
	v.syncViewerSize()
	v.viewerViewport.SetContent(clean)
	v.viewerViewport.GotoTop()
	return nil
}

func readTranscriptTail(path string, maxBytes int64) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if info.Size() <= maxBytes {
		b, err := io.ReadAll(f)
		return b, false, err
	}

	if _, err := f.Seek(-maxBytes, io.SeekEnd); err != nil {
		return nil, false, err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxBytes))
	return b, true, err
}

func (v *LogsView) syncViewerSize() {
	v.viewerViewport.Width = clampInt(v.width-12, 30, 92)
	v.viewerViewport.Height = clampInt(v.height-8, 6, 24)
}

func (v *LogsView) updateViewerModal(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		v.viewerOpen = false
		v.viewerText = ""
		v.viewerViewport.SetContent("")
		return nil
	case "c":
		if v.viewerText != "" {
			method, err := clipboard.Write(v.viewerText)
			if err != nil {
				return toastCmd(components.ToastError, err.Error())
			}
			return toastCmd(components.ToastSuccess, fmt.Sprintf("Transcript copied (%s)", method))
		}
		return nil
	}
	var cmd tea.Cmd
	v.viewerViewport, cmd = v.viewerViewport.Update(msg)
	return cmd
}

func (v *LogsView) ViewMain(w, h int) string {
	filterBadge := ""
	if v.hostFilter != "" {
		filterBadge = "Host: " + v.hostFilter
	}
	hdr, layout := RenderWorkspaceHeader(w, v.searching, v.searchInput, v.searchQuery, "", filterBadge)
	v.headerLayout = layout

	cols := []Column{
		{Title: "Time", Weight: 2, MinWidth: 14, MaxWidth: 18},
		{Title: "Host", Weight: 2, MinWidth: 10},
		{Title: "Target", Weight: 3, MinWidth: 14},
		{Title: "Status", Weight: 1, MinWidth: 8, MaxWidth: 10},
		{Title: "Duration", Weight: 1, MinWidth: 8, MaxWidth: 10},
		{Title: "Rec", Weight: 1, MinWidth: 4, MaxWidth: 5},
	}

	rows := make([][]string, len(v.filtered))
	statuses := make([]string, len(v.filtered))
	for i, l := range v.filtered {
		statuses[i] = l.Status
		rec := "-"
		if strings.TrimSpace(l.TranscriptPath) != "" {
			rec = "●"
		}
		ts := "-"
		if !l.StartedAt.IsZero() {
			ts = l.StartedAt.Local().Format("01-02 15:04:05")
		}
		rows[i] = []string{ts, l.HostName, l.TargetAddr, l.Status, HumanDuration(l.DurationSec), rec}
	}

	styler := func(rowIdx, colIdx int, paddedCell string) string {
		if colIdx == 3 && rowIdx < len(statuses) {
			if statuses[rowIdx] == db.LogStatusSuccess {
				return theme.StatusActive.Render(paddedCell)
			}
			return theme.StatusError.Render(paddedCell)
		}
		if colIdx == 5 && strings.HasPrefix(paddedCell, "●") {
			return theme.StatusStarting.Render(paddedCell)
		}
		return theme.RowNormal.Render(paddedCell)
	}

	emptyMsg := "No connection logs yet. Connect to a host to record audit history."
	if strings.TrimSpace(v.searchQuery) != "" || v.searching || v.hostFilter != "" {
		emptyMsg = "No logs match the current filter."
	}

	tw, th := TablePaneInnerSize(w, h)
	tableStr, newOff := RenderTableStyled(tw, th, cols, rows, v.cursor, v.offset, emptyMsg, styler)
	v.offset = newOff
	return RenderWorkspacePane(w, h, hdr, v.searching, tableStr)
}

func (v *LogsView) ViewInspector(w, h int) string {
	l, ok := v.selected()
	if !ok {
		return RenderInspectorLines(w, h, "LOG DETAIL", []string{theme.InspectorMuted.Render("Select a connection log to inspect.")})
	}

	statusStyled := theme.StatusError.Background(theme.Mantle).Render(l.Status)
	if l.Status == db.LogStatusSuccess {
		statusStyled = theme.StatusActive.Background(theme.Mantle).Render(l.Status)
	}
	rec := "None (enable Record Session on host)"
	if strings.TrimSpace(l.TranscriptPath) != "" {
		rec = l.TranscriptPath + " (Press [Enter] to view)"
	}

	lines := []string{
		FormatKV("Started", l.StartedAt.Local().Format("2006-01-02 15:04:05"), w) + theme.SpaceBg(theme.Mantle, 3) + theme.InspectorLabel.Render("Status: ") + statusStyled,
		FormatKV("Target", l.TargetAddr, w) + theme.SpaceBg(theme.Mantle, 3) + theme.InspectorLabel.Render("Duration: ") + theme.InspectorValue.Render(HumanDuration(l.DurationSec)),
		FormatKV("Transcript", rec, w),
	}
	return RenderInspectorLines(w, h, "AUDIT LOG: "+l.HostName, lines)
}

func (v *LogsView) ViewModal(fullW, fullH int) string {
	if v.confirm.Active {
		return v.confirm.View(fullW, fullH)
	}
	if !v.viewerOpen {
		return ""
	}
	v.syncViewerSize()
	meta := fmt.Sprintf("%s (%s)  •  %s  •  Duration: %s",
		v.viewerLog.HostName,
		v.viewerLog.TargetAddr,
		v.viewerLog.StartedAt.Local().Format("2006-01-02 15:04:05"),
		HumanDuration(v.viewerLog.DurationSec),
	)
	body := theme.InspectorLabel.Background(theme.Base).Render(components.Truncate(meta, v.viewerViewport.Width)) +
		"\n" + theme.TableSeparator.Render(strings.Repeat("─", minInt(v.viewerViewport.Width, fullW-10))) +
		"\n" + RenderCleanMultiline(v.viewerViewport.View(), v.viewerViewport.Width, theme.Base)
	footer := theme.InputHint.Render(fmt.Sprintf("[j/k/PgUp/PgDn] Scroll (%.0f%%)  [c] Copy  [Esc] Close", v.viewerViewport.ScrollPercent()*100))
	return components.RenderModal(fullW, fullH, "Session Transcript", body, footer, 100)
}

func (v *LogsView) Hints() []components.KeyHint {
	if v.searching {
		return []components.KeyHint{{Key: "Enter", Desc: "Apply"}, {Key: "Esc", Desc: "Clear"}}
	}
	return []components.KeyHint{
		{Key: "Enter", Desc: "View Transcript"},
		{Key: "f", Desc: "Filter Host"},
		{Key: "C", Desc: "Clear >30d"},
		{Key: "/", Desc: "Search"},
		{Key: "i", Desc: "Inspector"},
		{Key: "q", Desc: "Quit"},
	}
}
