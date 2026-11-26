package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ib-jpg/ez-ssh/internal/ssh"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

// KnownHostsView implements Modul 5: native ~/.ssh/known_hosts parser,
// fuzzy search, and safe atomic entry removal with backup.
type KnownHostsView struct {
	knownHostsPath string

	all      []ssh.KnownHostEntry
	filtered []ssh.KnownHostEntry

	cursor int
	offset int
	width  int
	height int

	searching    bool
	searchInput  textinput.Model
	searchQuery  string
	headerLayout HeaderLayout

	confirm     components.ConfirmModel
	deleteEntry *ssh.KnownHostEntry
}

// NewKnownHostsView creates a KnownHostsView for knownHostsPath.
func NewKnownHostsView(knownHostsPath string) *KnownHostsView {
	v := &KnownHostsView{
		knownHostsPath: knownHostsPath,
		searchInput:    NewStyledInput("Search host / fingerprint...", 64),
	}
	_ = v.Reload()
	return v
}

func (v *KnownHostsView) Init() tea.Cmd { return nil }

func (v *KnownHostsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

func (v *KnownHostsView) IsModalOpen() bool {
	return v.confirm.Active
}

func (v *KnownHostsView) IsSearching() bool {
	return v.searching
}

// Count returns total parsed known_hosts entries for sidebar badges.
func (v *KnownHostsView) Count() int {
	return len(v.all)
}

func (v *KnownHostsView) Reload() error {
	entries, err := ssh.ParseKnownHosts(v.knownHostsPath)
	if err != nil {
		return err
	}
	v.all = entries
	v.applyFilter()
	return nil
}

func (v *KnownHostsView) applyFilter() {
	q := v.searchQuery
	if v.searching {
		q = v.searchInput.Value()
	}
	v.filtered = v.filtered[:0]
	for _, e := range v.all {
		if components.FuzzyMatch(q, e.Hosts, e.KeyType, e.Fingerprint, e.Comment) {
			v.filtered = append(v.filtered, e)
		}
	}
	if len(v.filtered) == 0 {
		v.cursor = 0
		v.offset = 0
	} else if v.cursor >= len(v.filtered) {
		v.cursor = len(v.filtered) - 1
	}
}

func (v *KnownHostsView) selected() (ssh.KnownHostEntry, bool) {
	if len(v.filtered) == 0 || v.cursor < 0 || v.cursor >= len(v.filtered) {
		return ssh.KnownHostEntry{}, false
	}
	return v.filtered[v.cursor], true
}

func (v *KnownHostsView) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.MouseMsg:
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
				return v.executeDelete()
			}
			return nil
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
		case "r":
			if err := v.Reload(); err != nil {
				return toastCmd(components.ToastError, fmt.Sprintf("Reload known_hosts: %v", err))
			}
			return tea.Batch(
				func() tea.Msg { return DataChangedMsg{} },
				toastCmd(components.ToastInfo, fmt.Sprintf("Reloaded %d known_hosts entries", len(v.all))),
			)
		case "d":
			if e, ok := v.selected(); ok {
				copyEntry := e
				v.deleteEntry = &copyEntry
				label := e.Hosts
				if e.IsHashed {
					label = fmt.Sprintf("hashed host (line %d)", e.LineIndex+1)
				}
				v.confirm.Open(
					"delete-kh",
					"Remove Known Host Entry",
					fmt.Sprintf("Remove %s (%s) from known_hosts?\nA backup copy will be saved to known_hosts.bak.", label, e.KeyType),
					true,
				)
			}
		}
	}
	return nil
}

func (v *KnownHostsView) HandleClick(innerX, innerY int, _ bool) tea.Cmd {
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
		}
	}
	return nil
}

func (v *KnownHostsView) moveCursor(delta int) {
	if len(v.filtered) == 0 {
		v.cursor = 0
		return
	}
	v.cursor = clampInt(v.cursor+delta, 0, len(v.filtered)-1)
}

func (v *KnownHostsView) executeDelete() tea.Cmd {
	if v.deleteEntry == nil {
		return nil
	}
	entry := *v.deleteEntry
	v.deleteEntry = nil

	if err := ssh.DeleteKnownHostEntry(v.knownHostsPath, entry); err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Delete failed: %v", err))
	}
	_ = v.Reload()
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, "Removed entry (backup saved to known_hosts.bak)"),
	)
}

func (v *KnownHostsView) ViewMain(w, h int) string {
	hdr, layout := RenderWorkspaceHeader(w, v.searching, v.searchInput, v.searchQuery, "", "")
	v.headerLayout = layout

	cols := []Column{
		{Title: "Host / IP", Weight: 4, MinWidth: 16},
		{Title: "Key Type", Weight: 2, MinWidth: 12, MaxWidth: 22},
		{Title: "Fingerprint", Weight: 4, MinWidth: 16},
	}

	rows := make([][]string, len(v.filtered))
	for i, e := range v.filtered {
		hostDisp := e.Hosts
		if e.IsHashed {
			hostDisp = "[hashed] " + hostDisp
		}
		rows[i] = []string{hostDisp, e.KeyType, e.Fingerprint}
	}

	emptyMsg := "No entries in ~/.ssh/known_hosts."
	if strings.TrimSpace(v.searchQuery) != "" || v.searching {
		emptyMsg = "No known_hosts entries match your search."
	}

	tw, th := TablePaneInnerSize(w, h)
	tableStr, newOff := RenderTable(tw, th, cols, rows, v.cursor, v.offset, emptyMsg)
	v.offset = newOff
	return RenderWorkspacePane(w, h, hdr, v.searching, tableStr)
}

func (v *KnownHostsView) ViewInspector(w, h int) string {
	e, ok := v.selected()
	if !ok {
		return RenderInspectorLines(w, h, "KNOWN HOST DETAIL", []string{theme.InspectorMuted.Render("Select a known_hosts entry to inspect.")})
	}

	hostInfo := e.Hosts
	if e.IsHashed {
		hostInfo += " (Hashed |1|)"
	}
	lines := []string{
		FormatKV("Host", hostInfo, w),
		FormatKV("Key Type", e.KeyType, w) + theme.SpaceBg(theme.Mantle, 3) + theme.InspectorLabel.Render("Line: ") + theme.InspectorValue.Render(fmt.Sprintf("%d", e.LineIndex+1)),
		FormatKV("SHA256", e.Fingerprint, w),
	}
	if e.Marker != "" || e.Comment != "" {
		lines = append(lines, FormatKV("Meta", strings.TrimSpace(e.Marker+" "+e.Comment), w))
	}
	return RenderInspectorLines(w, h, "KNOWN HOST ENTRY", lines)
}

func (v *KnownHostsView) ViewModal(fullW, fullH int) string {
	if v.confirm.Active {
		return v.confirm.View(fullW, fullH)
	}
	return ""
}

func (v *KnownHostsView) Hints() []components.KeyHint {
	if v.searching {
		return []components.KeyHint{{Key: "Enter", Desc: "Apply"}, {Key: "Esc", Desc: "Clear"}}
	}
	return []components.KeyHint{
		{Key: "d", Desc: "Delete Entry"},
		{Key: "r", Desc: "Refresh"},
		{Key: "/", Desc: "Search"},
		{Key: "i", Desc: "Inspector"},
		{Key: "Tab", Desc: "Module"},
		{Key: "q", Desc: "Quit"},
	}
}
