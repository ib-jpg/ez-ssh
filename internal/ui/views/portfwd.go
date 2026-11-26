package views

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ssh"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

const (
	pfFieldName = iota
	pfFieldHost
	pfFieldType
	pfFieldLocalAddr
	pfFieldLocalPort
	pfFieldRemoteAddr
	pfFieldRemotePort
)

// PortFwdView implements Modul 3: Local/Remote/Dynamic SSH port forwarding tunnels.
type PortFwdView struct {
	store     *db.Store
	tunnelMgr *ssh.TunnelManager

	all      []db.PortForward
	filtered []db.PortForward
	hosts    []db.Host

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

	modalOpen   bool
	editingID   int64
	formFocus   int
	hostIdx     int
	tunnelType  string
	formErr     string
	inName      textinput.Model
	inLocalAddr textinput.Model
	inLocalPort textinput.Model
	inRemAddr   textinput.Model
	inRemPort   textinput.Model
}

// NewPortFwdView constructs a PortFwdView backed by store and tunnelMgr.
func NewPortFwdView(store *db.Store, tunnelMgr *ssh.TunnelManager) *PortFwdView {
	v := &PortFwdView{
		store:       store,
		tunnelMgr:   tunnelMgr,
		searchInput: NewStyledInput("Search tunnels...", 64),
		inName:      NewStyledInput("postgres-tunnel", 64),
		inLocalAddr: NewStyledInput("127.0.0.1", 64),
		inLocalPort: NewStyledInput("5432", 5),
		inRemAddr:   NewStyledInput("127.0.0.1", 128),
		inRemPort:   NewStyledInput("5432", 5),
	}
	_ = v.Reload()
	return v
}

func (v *PortFwdView) Init() tea.Cmd { return nil }

func (v *PortFwdView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

func (v *PortFwdView) IsModalOpen() bool {
	return v.modalOpen || v.confirm.Active
}

func (v *PortFwdView) IsSearching() bool {
	return v.searching
}

func (v *PortFwdView) Reload() error {
	pfs, err := v.store.ListPortForwards()
	if err != nil {
		return err
	}
	hosts, err := v.store.ListHosts()
	if err != nil {
		return err
	}
	v.all = pfs
	v.hosts = hosts
	v.applyFilter()
	return nil
}

func (v *PortFwdView) applyFilter() {
	q := v.searchQuery
	if v.searching {
		q = v.searchInput.Value()
	}
	v.filtered = v.filtered[:0]
	for _, pf := range v.all {
		if components.FuzzyMatch(q, pf.Name, pf.HostName, pf.HostTarget, pf.Type, strconv.Itoa(pf.LocalPort), strconv.Itoa(pf.RemotePort)) {
			v.filtered = append(v.filtered, pf)
		}
	}
	if len(v.filtered) == 0 {
		v.cursor = 0
		v.offset = 0
	} else if v.cursor >= len(v.filtered) {
		v.cursor = len(v.filtered) - 1
	}
}

func (v *PortFwdView) selected() (db.PortForward, bool) {
	if len(v.filtered) == 0 || v.cursor < 0 || v.cursor >= len(v.filtered) {
		return db.PortForward{}, false
	}
	return v.filtered[v.cursor], true
}

func (v *PortFwdView) Update(msg tea.Msg) tea.Cmd {
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
		case "g", "home":
			v.cursor = 0
			v.offset = 0
		case "G", "end":
			if len(v.filtered) > 0 {
				v.cursor = len(v.filtered) - 1
			}
		case " ", "enter":
			return v.toggleSelected()
		case "n":
			return v.openNewModal()
		case "e":
			return v.openEditModal()
		case "d":
			if pf, ok := v.selected(); ok {
				v.deleteTargetID = pf.ID
				v.confirm.Open("delete-pf", "Delete Port Forward", fmt.Sprintf("Delete tunnel %q (%s)?", pf.Name, formatTunnelMapping(pf)), true)
			}
		}
	}
	return nil
}

func (v *PortFwdView) HandleClick(innerX, innerY int, doubleClick bool) tea.Cmd {
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
				return v.toggleSelected()
			}
		}
	}
	return nil
}

func (v *PortFwdView) moveCursor(delta int) {
	if len(v.filtered) == 0 {
		v.cursor = 0
		return
	}
	v.cursor = clampInt(v.cursor+delta, 0, len(v.filtered)-1)
}

func (v *PortFwdView) toggleSelected() tea.Cmd {
	pf, ok := v.selected()
	if !ok {
		return nil
	}
	if v.tunnelMgr.IsRunning(pf.ID) {
		v.tunnelMgr.Stop(pf.ID)
		return toastCmd(components.ToastInfo, fmt.Sprintf("Stopped tunnel %s", pf.Name))
	}

	host, err := v.store.GetHost(pf.HostID)
	if err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Load host: %v", err))
	}
	var kc *db.KeychainItem
	if host.KeychainID != nil {
		kc, err = v.store.GetKeychain(*host.KeychainID)
		if err != nil {
			return toastCmd(components.ToastError, fmt.Sprintf("Load keychain: %v", err))
		}
	}

	if err := v.tunnelMgr.Start(pf, *host, kc); err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Start tunnel: %v", err))
	}
	return toastCmd(components.ToastInfo, fmt.Sprintf("Starting tunnel %s...", pf.Name))
}

func (v *PortFwdView) executeDelete() tea.Cmd {
	id := v.deleteTargetID
	v.deleteTargetID = 0
	v.tunnelMgr.Stop(id)
	if err := v.store.DeletePortForward(id); err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Delete tunnel: %v", err))
	}
	_ = v.Reload()
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, "Port forward deleted"),
	)
}

func (v *PortFwdView) openNewModal() tea.Cmd {
	hosts, _ := v.store.ListHosts()
	v.hosts = hosts
	if len(v.hosts) == 0 {
		return toastCmd(components.ToastInfo, "Create an SSH Host first before adding a tunnel")
	}

	v.modalOpen = true
	v.editingID = 0
	v.formFocus = 0
	v.hostIdx = 0
	v.tunnelType = db.TunnelLocal
	v.formErr = ""
	v.inName.SetValue("")
	v.inLocalAddr.SetValue("127.0.0.1")
	v.inLocalPort.SetValue("")
	v.inRemAddr.SetValue("127.0.0.1")
	v.inRemPort.SetValue("")
	v.syncModalFocus()
	return nil
}

func (v *PortFwdView) openEditModal() tea.Cmd {
	pf, ok := v.selected()
	if !ok {
		return nil
	}
	hosts, _ := v.store.ListHosts()
	v.hosts = hosts

	v.modalOpen = true
	v.editingID = pf.ID
	v.formFocus = 0
	v.hostIdx = 0
	for i, h := range v.hosts {
		if h.ID == pf.HostID {
			v.hostIdx = i
			break
		}
	}
	v.tunnelType = pf.Type
	v.formErr = ""
	v.inName.SetValue(pf.Name)
	v.inLocalAddr.SetValue(pf.LocalAddr)
	v.inLocalPort.SetValue(strconv.Itoa(pf.LocalPort))
	if pf.RemoteAddr != "" {
		v.inRemAddr.SetValue(pf.RemoteAddr)
	} else {
		v.inRemAddr.SetValue("127.0.0.1")
	}
	if pf.RemotePort > 0 {
		v.inRemPort.SetValue(strconv.Itoa(pf.RemotePort))
	} else {
		v.inRemPort.SetValue("")
	}
	v.syncModalFocus()
	return nil
}

func (v *PortFwdView) activeFormFields() []int {
	if v.tunnelType == db.TunnelDynamic {
		return []int{pfFieldName, pfFieldHost, pfFieldType, pfFieldLocalAddr, pfFieldLocalPort}
	}
	return []int{pfFieldName, pfFieldHost, pfFieldType, pfFieldLocalAddr, pfFieldLocalPort, pfFieldRemoteAddr, pfFieldRemotePort}
}

func (v *PortFwdView) syncModalFocus() {
	for _, ti := range []*textinput.Model{&v.inName, &v.inLocalAddr, &v.inLocalPort, &v.inRemAddr, &v.inRemPort} {
		ti.Blur()
	}
	fields := v.activeFormFields()
	v.formFocus = clampInt(v.formFocus, 0, len(fields)-1)
	switch fields[v.formFocus] {
	case pfFieldName:
		v.inName.Focus()
	case pfFieldLocalAddr:
		v.inLocalAddr.Focus()
	case pfFieldLocalPort:
		v.inLocalPort.Focus()
	case pfFieldRemoteAddr:
		v.inRemAddr.Focus()
	case pfFieldRemotePort:
		v.inRemPort.Focus()
	}
}

func (v *PortFwdView) updateModal(msg tea.KeyMsg) tea.Cmd {
	fields := v.activeFormFields()
	cur := fields[v.formFocus]

	switch msg.String() {
	case "esc":
		v.modalOpen = false
		return nil
	case "tab", "down":
		v.formFocus = (v.formFocus + 1) % len(fields)
		v.syncModalFocus()
		return nil
	case "shift+tab", "up":
		v.formFocus = (v.formFocus - 1 + len(fields)) % len(fields)
		v.syncModalFocus()
		return nil
	case "ctrl+s", "enter":
		return v.saveModal()
	}

	if cur == pfFieldHost {
		if len(v.hosts) > 0 {
			switch msg.String() {
			case "left", "h":
				v.hostIdx = (v.hostIdx - 1 + len(v.hosts)) % len(v.hosts)
			case "right", "l", " ":
				v.hostIdx = (v.hostIdx + 1) % len(v.hosts)
			}
		}
		return nil
	}
	if cur == pfFieldType {
		types := []string{db.TunnelLocal, db.TunnelRemote, db.TunnelDynamic}
		curIdx := 0
		for i, t := range types {
			if t == v.tunnelType {
				curIdx = i
				break
			}
		}
		switch msg.String() {
		case "left", "h":
			v.tunnelType = types[(curIdx+2)%3]
			v.syncModalFocus()
		case "right", "l", " ":
			v.tunnelType = types[(curIdx+1)%3]
			v.syncModalFocus()
		}
		return nil
	}

	var cmd tea.Cmd
	switch cur {
	case pfFieldName:
		v.inName, cmd = v.inName.Update(msg)
	case pfFieldLocalAddr:
		v.inLocalAddr, cmd = v.inLocalAddr.Update(msg)
	case pfFieldLocalPort:
		v.inLocalPort, cmd = v.inLocalPort.Update(msg)
	case pfFieldRemoteAddr:
		v.inRemAddr, cmd = v.inRemAddr.Update(msg)
	case pfFieldRemotePort:
		v.inRemPort, cmd = v.inRemPort.Update(msg)
	}
	return cmd
}

func (v *PortFwdView) saveModal() tea.Cmd {
	name := strings.TrimSpace(v.inName.Value())
	if name == "" {
		v.formErr = "Tunnel name is required."
		return nil
	}
	if len(v.hosts) == 0 {
		v.formErr = "Select a target SSH host."
		return nil
	}

	localPort, err := strconv.Atoi(strings.TrimSpace(v.inLocalPort.Value()))
	if err != nil || localPort < 1 || localPort > 65535 {
		v.formErr = "Local port must be between 1 and 65535."
		return nil
	}

	var remPort int
	if v.tunnelType != db.TunnelDynamic {
		remPort, err = strconv.Atoi(strings.TrimSpace(v.inRemPort.Value()))
		if err != nil || remPort < 1 || remPort > 65535 {
			v.formErr = "Remote port must be between 1 and 65535."
			return nil
		}
	}

	pf := &db.PortForward{
		ID:         v.editingID,
		Name:       name,
		HostID:     v.hosts[v.hostIdx].ID,
		Type:       v.tunnelType,
		LocalAddr:  strings.TrimSpace(v.inLocalAddr.Value()),
		LocalPort:  localPort,
		RemoteAddr: strings.TrimSpace(v.inRemAddr.Value()),
		RemotePort: remPort,
	}

	if v.editingID > 0 {
		v.tunnelMgr.Stop(v.editingID)
		err = v.store.UpdatePortForward(pf)
	} else {
		err = v.store.CreatePortForward(pf)
	}
	if err != nil {
		v.formErr = err.Error()
		return nil
	}

	v.modalOpen = false
	_ = v.Reload()
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, "Port forward saved"),
	)
}

func (v *PortFwdView) ViewMain(w, h int) string {
	hdr, layout := RenderWorkspaceHeader(w, v.searching, v.searchInput, v.searchQuery, "New", "")
	v.headerLayout = layout

	cols := []Column{
		{Title: "Status", Weight: 1, MinWidth: 10, MaxWidth: 12},
		{Title: "Name", Weight: 2, MinWidth: 10},
		{Title: "Mode", Weight: 1, MinWidth: 4, MaxWidth: 6},
		{Title: "Mapping", Weight: 4, MinWidth: 18},
		{Title: "Via Host", Weight: 2, MinWidth: 10},
	}

	rows := make([][]string, len(v.filtered))
	states := make([]ssh.TunnelState, len(v.filtered))
	for i, pf := range v.filtered {
		st := v.tunnelMgr.Status(pf.ID)
		states[i] = st.State
		rows[i] = []string{
			tunnelStateLabel(st.State),
			pf.Name,
			tunnelTypeShort(pf.Type),
			formatTunnelMapping(pf),
			pf.HostName,
		}
	}

	styler := func(rowIdx, colIdx int, paddedCell string) string {
		if colIdx == 0 && rowIdx < len(states) {
			switch states[rowIdx] {
			case ssh.TunnelActive:
				return theme.StatusActive.Render(paddedCell)
			case ssh.TunnelStarting:
				return theme.StatusStarting.Render(paddedCell)
			case ssh.TunnelError:
				return theme.StatusError.Render(paddedCell)
			default:
				return theme.StatusStopped.Render(paddedCell)
			}
		}
		return theme.RowNormal.Render(paddedCell)
	}

	emptyMsg := "No port forwards yet. Press [n] to create one, [Space] to toggle."
	if strings.TrimSpace(v.searchQuery) != "" || v.searching {
		emptyMsg = "No tunnels match your search."
	}

	tw, th := TablePaneInnerSize(w, h)
	tableStr, newOff := RenderTableStyled(tw, th, cols, rows, v.cursor, v.offset, emptyMsg, styler)
	v.offset = newOff
	return RenderWorkspacePane(w, h, hdr, v.searching, tableStr)
}

func (v *PortFwdView) ViewInspector(w, h int) string {
	pf, ok := v.selected()
	if !ok {
		return RenderInspectorLines(w, h, "TUNNEL DETAIL", []string{theme.InspectorMuted.Render("Select a tunnel and press [Space] to toggle.")})
	}

	st := v.tunnelMgr.Status(pf.ID)
	statusText := theme.StatusStopped.Background(theme.Mantle).Render(tunnelStateLabel(st.State))
	switch st.State {
	case ssh.TunnelActive:
		uptime := HumanDuration(int(time.Since(st.StartedAt).Seconds()))
		statusText = theme.StatusActive.Background(theme.Mantle).Render(fmt.Sprintf("● ACTIVE (%s, %d conns)", uptime, st.ActiveConns))
	case ssh.TunnelStarting:
		statusText = theme.StatusStarting.Background(theme.Mantle).Render("◐ CONNECTING...")
	case ssh.TunnelError:
		statusText = theme.StatusError.Background(theme.Mantle).Render("✖ ERROR")
	}

	lines := []string{
		theme.InspectorLabel.Render("Status: ") + statusText + theme.SpaceBg(theme.Mantle, 3) + theme.InspectorLabel.Render("Type: ") + theme.InspectorValue.Render(tunnelTypeLabel(pf.Type)),
		FormatKV("Mapping", formatTunnelMapping(pf), w),
		FormatKV("Via Host", fmt.Sprintf("%s (%s)", pf.HostName, pf.HostTarget), w),
	}
	if st.State == ssh.TunnelError && st.Error != "" {
		lines = append(lines, theme.StatusError.Background(theme.Mantle).Render(components.Truncate("Error: "+st.Error, w)))
	}

	return RenderInspectorLines(w, h, "TUNNEL: "+pf.Name, lines)
}

func (v *PortFwdView) ViewModal(fullW, fullH int) string {
	if v.confirm.Active {
		return v.confirm.View(fullW, fullH)
	}
	if !v.modalOpen {
		return ""
	}

	title := "New Port Forward"
	if v.editingID > 0 {
		title = "Edit Port Forward"
	}

	inW := clampInt(fullW-28, 16, 42)
	var rows []string
	for idx, f := range v.activeFormFields() {
		focused := idx == v.formFocus
		lbl := func(s string) string {
			p := components.PadRight(s, 14)
			if focused {
				return theme.InputLabel.Render("▸ " + p)
			}
			return theme.InspectorLabel.Background(theme.Base).Render("  " + p)
		}
		switch f {
		case pfFieldName:
			rows = append(rows, lbl("Name*")+RenderCleanTextInput(&v.inName, inW, theme.Base))
		case pfFieldHost:
			hLabel := "(no hosts)"
			if len(v.hosts) > 0 {
				h := v.hosts[v.hostIdx]
				hLabel = fmt.Sprintf("◄ %s (%s@%s) ►", h.Name, h.Username, h.Hostname)
			}
			rows = append(rows, lbl("SSH Host*")+theme.TagPill.Render(hLabel))
		case pfFieldType:
			rows = append(rows, lbl("Tunnel Type")+theme.ActionPill.Render("◄ "+tunnelTypeLabel(v.tunnelType)+" ►"))
		case pfFieldLocalAddr:
			rows = append(rows, lbl("Local Addr")+RenderCleanTextInput(&v.inLocalAddr, inW, theme.Base))
		case pfFieldLocalPort:
			rows = append(rows, lbl("Local Port*")+RenderCleanTextInput(&v.inLocalPort, inW, theme.Base))
		case pfFieldRemoteAddr:
			rows = append(rows, lbl("Remote Addr")+RenderCleanTextInput(&v.inRemAddr, inW, theme.Base))
		case pfFieldRemotePort:
			rows = append(rows, lbl("Remote Port*")+RenderCleanTextInput(&v.inRemPort, inW, theme.Base))
		}
	}
	if v.formErr != "" {
		rows = append(rows, "", theme.StatusError.Render("✖ "+v.formErr))
	}
	footer := theme.InputHint.Render("[Tab/↑↓] Field  [←/→] Select  [Enter] Save  [Esc] Cancel")
	return components.RenderModal(fullW, fullH, title, strings.Join(rows, "\n"), footer, 68)
}

func (v *PortFwdView) Hints() []components.KeyHint {
	if v.searching {
		return []components.KeyHint{{Key: "Enter", Desc: "Apply"}, {Key: "Esc", Desc: "Clear"}}
	}
	return []components.KeyHint{
		{Key: "Space", Desc: "Toggle ON/OFF"},
		{Key: "n", Desc: "New"},
		{Key: "e", Desc: "Edit"},
		{Key: "d", Desc: "Delete"},
		{Key: "/", Desc: "Search"},
		{Key: "q", Desc: "Quit"},
	}
}

func tunnelStateLabel(st ssh.TunnelState) string {
	switch st {
	case ssh.TunnelActive:
		return "● ACTIVE"
	case ssh.TunnelStarting:
		return "◐ STARTING"
	case ssh.TunnelError:
		return "✖ ERROR"
	default:
		return "○ STOPPED"
	}
}

func tunnelTypeShort(t string) string {
	switch t {
	case db.TunnelLocal:
		return "-L"
	case db.TunnelRemote:
		return "-R"
	case db.TunnelDynamic:
		return "-D"
	default:
		return t
	}
}

func tunnelTypeLabel(t string) string {
	switch t {
	case db.TunnelLocal:
		return "Local (-L)"
	case db.TunnelRemote:
		return "Remote (-R)"
	case db.TunnelDynamic:
		return "Dynamic (-D SOCKS5)"
	default:
		return t
	}
}

func formatTunnelMapping(pf db.PortForward) string {
	switch pf.Type {
	case db.TunnelDynamic:
		return fmt.Sprintf("%s:%d (SOCKS5)", pf.LocalAddr, pf.LocalPort)
	case db.TunnelRemote:
		return fmt.Sprintf("R:%s:%d → %s:%d", pf.RemoteAddr, pf.RemotePort, pf.LocalAddr, pf.LocalPort)
	default:
		return fmt.Sprintf("%s:%d → %s:%d", pf.LocalAddr, pf.LocalPort, pf.RemoteAddr, pf.RemotePort)
	}
}
