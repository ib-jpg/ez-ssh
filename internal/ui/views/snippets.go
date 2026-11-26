package views

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/clipboard"
	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ssh"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

// SnippetsView implements Modul 4: multi-line snippets CRUD, clipboard copy,
// and async remote SSH execution with scrollable output viewer.
type SnippetsView struct {
	store          *db.Store
	vault          *crypto.Vault
	knownHostsPath string

	all      []db.Snippet
	filtered []db.Snippet

	cursor int
	offset int
	width  int
	height int

	searching    bool
	searchInput  textinput.Model
	searchQuery  string
	headerLayout HeaderLayout

	confirm        components.ConfirmModel
	deleteTargetID int64

	// Add/Edit modal
	modalOpen  bool
	editingID  int64
	formFocus  int // 0=name, 1=category, 2=command textarea
	formErr    string
	inName     textinput.Model
	inCategory textinput.Model
	inCommand  textarea.Model

	// Host picker for Run on Host
	pickHostOpen bool
	pickHosts    []db.Host
	pickCursor   int
	runSnippet   db.Snippet

	// Remote execution output modal
	outputOpen     bool
	running        bool
	runSpinner     spinner.Model
	runHost        db.Host
	runOutText     string
	runDuration    time.Duration
	runErr         error
	outputSaved    bool
	outputViewport viewport.Model
}

// NewSnippetsView constructs a SnippetsView backed by store and vault.
func NewSnippetsView(store *db.Store, vault *crypto.Vault, knownHostsPath string) *SnippetsView {
	ta := textarea.New()
	ta.Placeholder = "docker ps --format 'table {{.Names}}\\t{{.Status}}'"
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.SetWidth(48)
	ta.SetHeight(6)
	ta.CharLimit = 8192
	// Bubbles' defaults leave text/cursor/end-of-buffer cells with no explicit
	// background, which would expose the terminal's default gray. Pin every
	// textarea surface to the modal's Base background.
	taBase := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base)
	ta.FocusedStyle.Base = taBase
	ta.BlurredStyle.Base = taBase
	ta.FocusedStyle.Text = taBase
	ta.BlurredStyle.Text = taBase
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(theme.Surface0)
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle().Background(theme.Base)
	ta.FocusedStyle.CursorLineNumber = lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base)
	ta.BlurredStyle.CursorLineNumber = lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base)
	ta.FocusedStyle.EndOfBuffer = lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base)
	ta.BlurredStyle.EndOfBuffer = lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base)
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base)
	ta.BlurredStyle.Placeholder = lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base)
	ta.FocusedStyle.Prompt = lipgloss.NewStyle().Background(theme.Base)
	ta.BlurredStyle.Prompt = lipgloss.NewStyle().Background(theme.Base)
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(theme.Blue).Background(theme.Base)
	ta.Cursor.TextStyle = lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(theme.Mauve).Background(theme.Base)

	v := &SnippetsView{
		store:          store,
		vault:          vault,
		knownHostsPath: knownHostsPath,
		searchInput:    NewStyledInput("Search snippets...", 64),
		inName:         NewStyledInput("Check Docker Containers", 64),
		inCategory:     NewStyledInput("Docker", 48),
		inCommand:      ta,
		runSpinner:     sp,
		outputViewport: viewport.New(60, 12),
	}
	v.outputViewport.MouseWheelEnabled = true
	_ = v.Reload()
	return v
}

func (v *SnippetsView) Init() tea.Cmd { return nil }

func (v *SnippetsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

func (v *SnippetsView) IsModalOpen() bool {
	return v.modalOpen || v.pickHostOpen || v.outputOpen || v.confirm.Active
}

func (v *SnippetsView) IsSearching() bool {
	return v.searching
}

func (v *SnippetsView) Reload() error {
	items, err := v.store.ListSnippets()
	if err != nil {
		return err
	}
	v.all = items
	v.applyFilter()
	return nil
}

func (v *SnippetsView) applyFilter() {
	q := v.searchQuery
	if v.searching {
		q = v.searchInput.Value()
	}
	v.filtered = v.filtered[:0]
	for _, sn := range v.all {
		if components.FuzzyMatch(q, sn.Name, sn.Category, sn.Command) {
			v.filtered = append(v.filtered, sn)
		}
	}
	if len(v.filtered) == 0 {
		v.cursor = 0
		v.offset = 0
	} else if v.cursor >= len(v.filtered) {
		v.cursor = len(v.filtered) - 1
	}
}

func (v *SnippetsView) selected() (db.Snippet, bool) {
	if len(v.filtered) == 0 || v.cursor < 0 || v.cursor >= len(v.filtered) {
		return db.Snippet{}, false
	}
	return v.filtered[v.cursor], true
}

func (v *SnippetsView) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case SnippetExecResultMsg:
		wasOpen := v.outputOpen
		v.running = false
		v.runDuration = m.Duration
		v.runErr = m.Err
		cleanOut := components.SanitizeTranscript([]byte(m.Output))
		if strings.TrimSpace(cleanOut) == "" && m.Err == nil {
			cleanOut = "(command exited with no output)"
		}
		if m.Err != nil {
			if cleanOut != "" {
				cleanOut += "\n\n"
			}
			cleanOut += "[Error] " + m.Err.Error()
		}
		v.runOutText = cleanOut
		v.syncOutputViewportSize()
		v.outputViewport.SetContent(cleanOut)
		v.outputViewport.GotoTop()
		if !wasOpen {
			if m.Err != nil {
				return toastCmd(components.ToastError, fmt.Sprintf("Snippet %s failed: %v", m.SnippetName, m.Err))
			}
			return toastCmd(components.ToastSuccess, fmt.Sprintf("Snippet %s finished on %s", m.SnippetName, m.Host.Name))
		}
		return nil

	case spinner.TickMsg:
		if v.outputOpen && v.running {
			var cmd tea.Cmd
			v.runSpinner, cmd = v.runSpinner.Update(m)
			return cmd
		}
		return nil

	case tea.MouseMsg:
		if v.outputOpen && !v.running {
			var cmd tea.Cmd
			v.outputViewport, cmd = v.outputViewport.Update(m)
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
				return v.executeDelete()
			}
			return nil
		}
		if v.outputOpen {
			return v.updateOutputModal(m)
		}
		if v.pickHostOpen {
			return v.updateHostPicker(m)
		}
		if v.modalOpen {
			return v.updateModal(m)
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
		case "home":
			v.cursor = 0
			v.offset = 0
		case "G", "end":
			if len(v.filtered) > 0 {
				v.cursor = len(v.filtered) - 1
			}
		case "n":
			return v.openNewModal()
		case "e", "enter":
			return v.openEditModal()
		case "c":
			if sn, ok := v.selected(); ok {
				method, err := clipboard.Write(sn.Command)
				if err != nil {
					return toastCmd(components.ToastError, err.Error())
				}
				return toastCmd(components.ToastSuccess, fmt.Sprintf("Snippet copied (%s)", method))
			}
		case "r":
			return v.openRunPicker()
		case "d":
			if sn, ok := v.selected(); ok {
				v.deleteTargetID = sn.ID
				v.confirm.Open("delete-snippet", "Delete Snippet", fmt.Sprintf("Delete snippet %q?", sn.Name), true)
			}
		}
	}
	return nil
}

func (v *SnippetsView) HandleClick(innerX, innerY int, doubleClick bool) tea.Cmd {
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
		if v.headerLayout.NewX0 >= 0 && innerX >= v.headerLayout.NewX0 && innerX < v.headerLayout.NewX1 {
			v.searching = false
			v.searchInput.Blur()
			return v.openNewModal()
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
				return v.openEditModal()
			}
		}
	}
	return nil
}

func (v *SnippetsView) moveCursor(delta int) {
	if len(v.filtered) == 0 {
		v.cursor = 0
		return
	}
	v.cursor = clampInt(v.cursor+delta, 0, len(v.filtered)-1)
}

func (v *SnippetsView) executeDelete() tea.Cmd {
	id := v.deleteTargetID
	v.deleteTargetID = 0
	if err := v.store.DeleteSnippet(id); err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Delete snippet: %v", err))
	}
	_ = v.Reload()
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, "Snippet deleted"),
	)
}

func (v *SnippetsView) openNewModal() tea.Cmd {
	v.modalOpen = true
	v.editingID = 0
	v.formFocus = 0
	v.formErr = ""
	v.inName.SetValue("")
	v.inCategory.SetValue("General")
	v.inCommand.SetValue("")
	v.syncModalFocus()
	return nil
}

func (v *SnippetsView) openEditModal() tea.Cmd {
	sn, ok := v.selected()
	if !ok {
		return nil
	}
	v.modalOpen = true
	v.editingID = sn.ID
	v.formFocus = 0
	v.formErr = ""
	v.inName.SetValue(sn.Name)
	v.inCategory.SetValue(sn.Category)
	v.inCommand.SetValue(sn.Command)
	v.syncModalFocus()
	return nil
}

func (v *SnippetsView) syncModalFocus() {
	v.inName.Blur()
	v.inCategory.Blur()
	v.inCommand.Blur()
	v.formFocus = clampInt(v.formFocus, 0, 2)
	switch v.formFocus {
	case 0:
		v.inName.Focus()
	case 1:
		v.inCategory.Focus()
	case 2:
		v.inCommand.Focus()
	}
}

func (v *SnippetsView) updateModal(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.modalOpen = false
		return nil
	case "ctrl+s":
		return v.saveModal()
	case "tab":
		v.formFocus = (v.formFocus + 1) % 3
		v.syncModalFocus()
		return nil
	case "shift+tab":
		v.formFocus = (v.formFocus + 2) % 3
		v.syncModalFocus()
		return nil
	}

	if v.formFocus < 2 {
		switch msg.String() {
		case "down", "enter":
			v.formFocus++
			v.syncModalFocus()
			return nil
		case "up":
			v.formFocus = (v.formFocus + 2) % 3
			v.syncModalFocus()
			return nil
		}
	}

	var cmd tea.Cmd
	switch v.formFocus {
	case 0:
		v.inName, cmd = v.inName.Update(msg)
	case 1:
		v.inCategory, cmd = v.inCategory.Update(msg)
	case 2:
		v.inCommand, cmd = v.inCommand.Update(msg)
	}
	return cmd
}

func (v *SnippetsView) saveModal() tea.Cmd {
	name := strings.TrimSpace(v.inName.Value())
	cmdText := strings.TrimSpace(v.inCommand.Value())
	if name == "" || cmdText == "" {
		v.formErr = "Snippet name and command are required."
		return nil
	}
	sn := &db.Snippet{
		ID:       v.editingID,
		Name:     name,
		Category: strings.TrimSpace(v.inCategory.Value()),
		Command:  cmdText,
	}
	var err error
	if v.editingID > 0 {
		err = v.store.UpdateSnippet(sn)
	} else {
		err = v.store.CreateSnippet(sn)
	}
	if err != nil {
		v.formErr = err.Error()
		return nil
	}
	v.modalOpen = false
	_ = v.Reload()
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, "Snippet saved"),
	)
}

func (v *SnippetsView) openRunPicker() tea.Cmd {
	sn, ok := v.selected()
	if !ok {
		return nil
	}
	hosts, err := v.store.ListHosts()
	if err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("List hosts: %v", err))
	}
	if len(hosts) == 0 {
		return toastCmd(components.ToastInfo, "No SSH hosts configured yet")
	}
	v.pickHostOpen = true
	v.pickHosts = hosts
	v.pickCursor = 0
	v.runSnippet = sn
	return nil
}

func (v *SnippetsView) updateHostPicker(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		v.pickHostOpen = false
		return nil
	case "j", "down":
		if len(v.pickHosts) > 0 {
			v.pickCursor = (v.pickCursor + 1) % len(v.pickHosts)
		}
	case "k", "up":
		if len(v.pickHosts) > 0 {
			v.pickCursor = (v.pickCursor - 1 + len(v.pickHosts)) % len(v.pickHosts)
		}
	case "enter":
		host := v.pickHosts[v.pickCursor]
		v.pickHostOpen = false
		return v.executeSnippetOnHost(host, v.runSnippet)
	}
	return nil
}

func (v *SnippetsView) executeSnippetOnHost(host db.Host, sn db.Snippet) tea.Cmd {
	var kc *db.KeychainItem
	if host.KeychainID != nil {
		item, err := v.store.GetKeychain(*host.KeychainID)
		if err != nil {
			return toastCmd(components.ToastError, fmt.Sprintf("Load keychain: %v", err))
		}
		kc = item
	}

	v.outputOpen = true
	v.running = true
	v.runHost = host
	v.runOutText = ""
	v.runDuration = 0
	v.runErr = nil
	v.outputSaved = false

	vault := v.vault
	khPath := v.knownHostsPath
	return tea.Batch(
		v.runSpinner.Tick,
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			out, dur, err := ssh.ExecuteSnippet(ctx, host, kc, vault, khPath, sn.Command)
			return SnippetExecResultMsg{
				SnippetName: sn.Name,
				Host:        host,
				Output:      out,
				Duration:    dur,
				Err:         err,
			}
		},
	)
}

func (v *SnippetsView) updateOutputModal(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		v.outputOpen = false
		return nil
	case "c":
		if !v.running && v.runOutText != "" {
			method, err := clipboard.Write(v.runOutText)
			if err != nil {
				return toastCmd(components.ToastError, err.Error())
			}
			return toastCmd(components.ToastSuccess, fmt.Sprintf("Output copied (%s)", method))
		}
		return nil
	case "s":
		if !v.running && !v.outputSaved {
			status := db.LogStatusSuccess
			if v.runErr != nil {
				status = db.LogStatusFailed
			}
			hid := v.runHost.ID
			logEntry := &db.ConnectionLog{
				HostID:      &hid,
				HostName:    v.runHost.Name,
				TargetAddr:  fmt.Sprintf("%s@%s:%d [snippet:%s]", v.runHost.Username, v.runHost.Hostname, v.runHost.Port, v.runSnippet.Name),
				Status:      status,
				DurationSec: maxInt(0, int(v.runDuration.Seconds())),
				StartedAt:   time.Now().UTC(),
			}
			if err := v.store.CreateLog(logEntry); err != nil {
				return toastCmd(components.ToastError, fmt.Sprintf("Save log: %v", err))
			}
			v.outputSaved = true
			return tea.Batch(
				func() tea.Msg { return DataChangedMsg{} },
				toastCmd(components.ToastSuccess, "Execution logged"),
			)
		}
		return nil
	}
	if !v.running {
		var cmd tea.Cmd
		v.outputViewport, cmd = v.outputViewport.Update(msg)
		return cmd
	}
	return nil
}

func (v *SnippetsView) syncOutputViewportSize() {
	vpW := clampInt(v.width-12, 30, 86)
	vpH := clampInt(v.height-8, 6, 22)
	v.outputViewport.Width = vpW
	v.outputViewport.Height = vpH
}

func (v *SnippetsView) ViewMain(w, h int) string {
	hdr, layout := RenderWorkspaceHeader(w, v.searching, v.searchInput, v.searchQuery, "New", "")
	v.headerLayout = layout

	cols := []Column{
		{Title: "Name", Weight: 2, MinWidth: 12},
		{Title: "Category", Weight: 1, MinWidth: 8, MaxWidth: 16},
		{Title: "Command", Weight: 5, MinWidth: 16},
	}

	rows := make([][]string, len(v.filtered))
	for i, sn := range v.filtered {
		preview := strings.Join(strings.Fields(sn.Command), " ")
		rows[i] = []string{sn.Name, sn.Category, preview}
	}

	emptyMsg := "No snippets saved. Press [n] to create a command snippet."
	if strings.TrimSpace(v.searchQuery) != "" || v.searching {
		emptyMsg = "No snippets match your search."
	}

	tw, th := TablePaneInnerSize(w, h)
	tableStr, newOff := RenderTable(tw, th, cols, rows, v.cursor, v.offset, emptyMsg)
	v.offset = newOff
	return RenderWorkspacePane(w, h, hdr, v.searching, tableStr)
}

func (v *SnippetsView) ViewInspector(w, h int) string {
	sn, ok := v.selected()
	if !ok {
		return RenderInspectorLines(w, h, "SNIPPET PREVIEW", []string{theme.InspectorMuted.Render("Select a snippet to preview its command.")})
	}

	lines := []string{
		FormatKV("Category", sn.Category, w),
	}
	cmdLines := strings.Split(strings.ReplaceAll(sn.Command, "\r\n", "\n"), "\n")
	for _, cl := range cmdLines {
		lines = append(lines, theme.InspectorValue.Render(cl))
	}
	return RenderInspectorLines(w, h, "SNIPPET: "+sn.Name, lines)
}

func (v *SnippetsView) ViewModal(fullW, fullH int) string {
	if v.confirm.Active {
		return v.confirm.View(fullW, fullH)
	}
	if v.outputOpen {
		if v.running {
			body := v.runSpinner.View() + lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).
				Render(fmt.Sprintf(" Running %q on %s (%s@%s)...", v.runSnippet.Name, v.runHost.Name, v.runHost.Username, v.runHost.Hostname))
			return components.RenderModal(fullW, fullH, "Snippet Output", body, theme.InputHint.Render("[Esc] Close"), 68)
		}
		v.syncOutputViewportSize()
		status := theme.StatusActive.Render("Success")
		if v.runErr != nil {
			status = theme.StatusError.Render("Failed")
		}
		sub := lipgloss.NewStyle().Foreground(theme.Subtext0).Background(theme.Base)
		meta := sub.Render(fmt.Sprintf("Host: %s (%s)  •  Status: ", v.runHost.Name, v.runHost.Hostname)) +
			status +
			sub.Render(fmt.Sprintf("  •  Duration: %s", v.runDuration.Round(time.Millisecond)))
		body := meta + "\n" + theme.TableSeparator.Render(strings.Repeat("─", minInt(v.outputViewport.Width, fullW-10))) + "\n" +
			RenderCleanMultiline(v.outputViewport.View(), v.outputViewport.Width, theme.Base)
		saveHint := "[s] Save to Logs"
		if v.outputSaved {
			saveHint = "✓ Logged"
		}
		footer := theme.InputHint.Render(fmt.Sprintf("[j/k/PgUp/PgDn] Scroll  [c] Copy  %s  [Esc] Close", saveHint))
		return components.RenderModal(fullW, fullH, "Output: "+v.runSnippet.Name, body, footer, 94)
	}
	if v.pickHostOpen {
		items := make([]string, len(v.pickHosts))
		for i, h := range v.pickHosts {
			items[i] = fmt.Sprintf("%s (%s@%s:%d)", h.Name, h.Username, h.Hostname, h.Port)
		}
		maxVis := clampInt(fullH-10, 4, 14)
		body := RenderPickerList(items, v.pickCursor, maxVis, 48)
		footer := theme.InputHint.Render("[j/k] Select Host  [Enter] Execute  [Esc] Cancel")
		return components.RenderModal(fullW, fullH, "Run Snippet: "+v.runSnippet.Name, body, footer, 60)
	}
	if !v.modalOpen {
		return ""
	}

	title := "New Snippet"
	if v.editingID > 0 {
		title = "Edit Snippet"
	}

	lbl := func(idx int, s string) string {
		p := components.PadRight(s, 12)
		if v.formFocus == idx {
			return theme.InputLabel.Render("▸ " + p)
		}
		return theme.InspectorLabel.Background(theme.Base).Render("  " + p)
	}

	taW := clampInt(fullW-16, 28, 56)
	inW := clampInt(taW-14, 16, 42)
	v.inCommand.SetWidth(taW)
	rows := []string{
		lbl(0, "Name*") + RenderCleanTextInput(&v.inName, inW, theme.Base),
		lbl(1, "Category") + RenderCleanTextInput(&v.inCategory, inW, theme.Base),
		lbl(2, "Command*"),
		RenderCleanMultiline(v.inCommand.View(), taW, theme.Base),
	}
	if v.formErr != "" {
		rows = append(rows, "", theme.StatusError.Render("✖ "+v.formErr))
	}
	footer := theme.InputHint.Render("[Tab] Next Field  [Ctrl+S] Save  [Esc] Cancel")
	return components.RenderModal(fullW, fullH, title, strings.Join(rows, "\n"), footer, 70)
}

func (v *SnippetsView) Hints() []components.KeyHint {
	if v.searching {
		return []components.KeyHint{{Key: "Enter", Desc: "Apply"}, {Key: "Esc", Desc: "Clear"}}
	}
	return []components.KeyHint{
		{Key: "r", Desc: "Run on Host"},
		{Key: "c", Desc: "Copy"},
		{Key: "n", Desc: "New"},
		{Key: "e", Desc: "Edit"},
		{Key: "d", Desc: "Delete"},
		{Key: "/", Desc: "Search"},
		{Key: "q", Desc: "Quit"},
	}
}
