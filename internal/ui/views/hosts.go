package views

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/config"
	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ssh"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

const (
	hostAuthPassword = 0
	hostAuthKeychain = 1
	hostAuthKeyPath  = 2
	hostAuthAgent    = 3
)

const (
	hfName = iota
	hfHostname
	hfPort
	hfUsername
	hfOS
	hfAuthMode
	hfAuthDetail
	hfGroup
	hfTags
	hfRecord
)

type rect struct {
	id int
	x0 int
	y0 int
	x1 int
	y1 int
}

func (r rect) contains(x, y int) bool {
	return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1
}

const (
	actConnect   = 1
	actSFTP      = 2
	actEdit      = 3
	actDelete    = 4
	actDuplicate = 5
	actMenu      = 6
	actCollapse  = 7
	actNewHost   = 8
	actQuickTerm = 9
	actImport    = 10
)

type dropdownEntry struct {
	id       int
	icon     string
	label    string
	shortcut string
	danger   bool
}

var hostMenuEntries = []dropdownEntry{
	{id: actConnect, icon: "󰆍", label: "Quick Connect", shortcut: "Enter"},
	{id: actSFTP, icon: "󰉋", label: "Open SFTP", shortcut: "s"},
	{id: actEdit, icon: "󰏫", label: "Edit Host Details", shortcut: "e"},
	{id: actDuplicate, icon: "󰆏", label: "Duplicate", shortcut: "c"},
	{id: actDelete, icon: "󰆴", label: "Remove", shortcut: "d", danger: true},
}

// HostsView implements Modul 1 with Termius-grade 3-layer visual hierarchy:
// full-width search bar, action toolbar, elevated host cards grid, floating
// context dropdown menu, structured right Host Details panel, and Pro host form.
type HostsView struct {
	store         *db.Store
	vault         *crypto.Vault
	sshConfigPath string

	all           []db.Host
	filtered      []db.Host
	keychains     []db.KeychainItem
	activeHostIDs map[int64]bool

	cursor        int
	rowOffset     int
	gridCols      int
	width         int
	height        int
	showInspector bool

	searching         bool
	searchInput       textinput.Model
	searchQuery       string
	searchBarRect     rect
	searchConnectRect rect
	toolbarRects      []rect
	cardRects         []rect
	actionRects       []rect

	// Floating context dropdown menu
	menuOpen      bool
	menuCursor    int
	menuAnchorX   int
	menuAnchorY   int
	menuRect      rect
	menuItemRects []rect

	// Quick Terminal ad-hoc SSH modal
	quickTermOpen  bool
	quickTermInput textinput.Model
	quickTermErr   string

	confirm        components.ConfirmModel
	deleteTargetID int64

	modalOpen            bool
	editingID            int64
	existingInlineSecret []byte
	existingNotes        string
	formFocus            int
	authMode             int
	osIdx                int
	kcIdx                int
	recordSession        bool
	formErr              string

	inName     textinput.Model
	inHostname textinput.Model
	inPort     textinput.Model
	inUsername textinput.Model
	inPassword textinput.Model
	inKeyPath  textinput.Model
	inGroup    textinput.Model
	inTags     textinput.Model
}

// NewHostsView constructs a HostsView backed by store and vault.
func NewHostsView(store *db.Store, vault *crypto.Vault, sshConfigPath string) *HostsView {
	si := NewStyledInput("Find a host or type user@hostname...", 96)
	qt := NewStyledInput("user@hostname[:port]", 128)

	v := &HostsView{
		store:          store,
		vault:          vault,
		sshConfigPath:  sshConfigPath,
		activeHostIDs:  make(map[int64]bool),
		gridCols:       2,
		showInspector:  true,
		searchInput:    si,
		quickTermInput: qt,
		inName:         NewStyledInput("prod-web-01", 64),
		inHostname:     NewStyledInput("192.168.1.50", 128),
		inPort:         NewStyledInput("22", 5),
		inUsername:     NewStyledInput("ubuntu", 64),
		inPassword:     NewStyledInput("password", 256),
		inKeyPath:      NewStyledInput("~/.ssh/id_ed25519", 256),
		inGroup:        NewStyledInput("General", 48),
		inTags:         NewStyledInput("docker,nginx", 128),
	}
	v.inPassword.EchoMode = textinput.EchoPassword
	v.inPassword.EchoCharacter = '•'
	_ = v.Reload()
	return v
}

func (v *HostsView) Init() tea.Cmd { return nil }

func (v *HostsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// UsesRightInspector reports that HostsView renders its own right-hand detail panel.
func (v *HostsView) UsesRightInspector() bool {
	return true
}

// SetShowInspector toggles visibility of the right-hand Host Details panel.
func (v *HostsView) SetShowInspector(show bool) {
	v.showInspector = show
}

// SetActiveHostIDs updates which hosts currently have an open embedded session tab.
func (v *HostsView) SetActiveHostIDs(ids map[int64]bool) {
	v.activeHostIDs = ids
}

func (v *HostsView) IsModalOpen() bool {
	return v.modalOpen || v.quickTermOpen || v.menuOpen || v.confirm.Active
}

// IsDropdownOpen reports whether the floating context dropdown menu is visible.
func (v *HostsView) IsDropdownOpen() bool {
	return v.menuOpen
}

func (v *HostsView) IsSearching() bool {
	return v.searching
}

func (v *HostsView) Reload() error {
	hosts, err := v.store.ListHosts()
	if err != nil {
		return err
	}
	kcs, err := v.store.ListKeychain()
	if err != nil {
		return err
	}
	v.all = hosts
	v.keychains = kcs
	v.applyFilter()
	return nil
}

func (v *HostsView) applyFilter() {
	q := v.searchQuery
	if v.searching {
		q = v.searchInput.Value()
	}
	v.filtered = v.filtered[:0]
	for _, h := range v.all {
		if components.FuzzyMatch(q, h.Name, h.Hostname, h.Username, h.GroupName, h.Tags, h.KeychainName, h.OSType) {
			v.filtered = append(v.filtered, h)
		}
	}
	if len(v.filtered) == 0 {
		v.cursor = 0
		v.rowOffset = 0
	} else if v.cursor >= len(v.filtered) {
		v.cursor = len(v.filtered) - 1
	}
}

func (v *HostsView) selected() (db.Host, bool) {
	if len(v.filtered) == 0 || v.cursor < 0 || v.cursor >= len(v.filtered) {
		return db.Host{}, false
	}
	return v.filtered[v.cursor], true
}

// OpenNew triggers the New Host modal externally (e.g. from top bar [+]).
func (v *HostsView) OpenNew() tea.Cmd {
	return v.openNewModal()
}

// OpenEditForHost selects hostID (clearing filter if needed) and opens the Edit Host modal.
func (v *HostsView) OpenEditForHost(hostID int64) bool {
	_ = v.Reload()
	idx := -1
	for i, h := range v.filtered {
		if h.ID == hostID {
			idx = i
			break
		}
	}
	if idx < 0 && v.searchQuery != "" {
		v.searchQuery = ""
		v.searchInput.SetValue("")
		v.applyFilter()
		for i, h := range v.filtered {
			if h.ID == hostID {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return false
	}
	v.cursor = idx
	v.openEditModal()
	return true
}

func (v *HostsView) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.MouseMsg:
		if v.modalOpen || v.quickTermOpen || v.confirm.Active {
			return nil
		}
		if v.menuOpen {
			if m.Action == tea.MouseActionPress && m.Button == tea.MouseButtonLeft {
				return v.HandleMouse(m.X, m.Y, m.Button, false)
			}
			return nil
		}
		if m.Button == tea.MouseButtonWheelUp {
			v.moveCursor(-maxInt(1, v.gridCols))
		} else if m.Button == tea.MouseButtonWheelDown {
			v.moveCursor(maxInt(1, v.gridCols))
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

		if v.quickTermOpen {
			return v.updateQuickTermModal(m)
		}

		if v.modalOpen {
			return v.updateModal(m)
		}

		if v.menuOpen {
			return v.updateContextMenu(m)
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
				raw := strings.TrimSpace(v.searchInput.Value())
				if host, ok := parseAdHocTarget(raw); ok {
					v.searching = false
					v.searchInput.Blur()
					return func() tea.Msg { return OpenSessionMsg{Host: host} }
				}
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

		cols := maxInt(1, v.gridCols)
		switch m.String() {
		case "esc":
			if v.searchQuery != "" {
				v.searchQuery = ""
				v.searchInput.SetValue("")
				v.applyFilter()
			}
			return nil
		case "/":
			v.searching = true
			v.searchInput.SetValue(v.searchQuery)
			v.searchInput.CursorEnd()
			return v.searchInput.Focus()
		case "right", "l":
			v.moveCursor(1)
		case "left", "h":
			v.moveCursor(-1)
		case "down", "j":
			v.moveCursor(cols)
		case "up", "k":
			v.moveCursor(-cols)
		case "g", "home":
			v.cursor = 0
			v.rowOffset = 0
		case "G", "end":
			if len(v.filtered) > 0 {
				v.cursor = len(v.filtered) - 1
			}
		case "enter":
			return v.connectSelectedOrAdHoc()
		case "m":
			if _, ok := v.selected(); ok {
				v.openContextMenuForSelected()
			}
		case "t":
			return v.openQuickTermModal()
		case "s", "S":
			return v.openSFTPSelected()
		case "n":
			return v.openNewModal()
		case "e":
			return v.openEditModal()
		case "d":
			return v.promptDeleteSelected()
		case "I":
			return v.importSSHConfig()
		case "c":
			return v.duplicateSelected()
		}
	}
	return nil
}

func (v *HostsView) HandleClick(innerX, innerY int, doubleClick bool) tea.Cmd {
	return v.HandleMouse(innerX, innerY, tea.MouseButtonLeft, doubleClick)
}

// HandleMouse handles left and right mouse clicks in HostsView coordinates.
func (v *HostsView) HandleMouse(innerX, innerY int, button tea.MouseButton, doubleClick bool) tea.Cmd {
	if v.modalOpen || v.quickTermOpen || v.confirm.Active {
		return nil
	}

	if v.menuOpen {
		for _, r := range v.menuItemRects {
			if r.contains(innerX, innerY) {
				v.menuOpen = false
				return v.executeMenuAction(r.id)
			}
		}
		// Click outside dropdown closes it cleanly.
		v.menuOpen = false
		return nil
	}

	if button == tea.MouseButtonRight {
		for _, r := range v.cardRects {
			if r.contains(innerX, innerY) && r.id >= 0 && r.id < len(v.filtered) {
				v.cursor = r.id
				v.openContextMenuAt(innerX+1, innerY+1)
				return nil
			}
		}
		return nil
	}

	if button != tea.MouseButtonLeft {
		return nil
	}

	// Search bar & Connect button
	if v.searchConnectRect.contains(innerX, innerY) {
		v.searching = false
		v.searchInput.Blur()
		return v.connectSelectedOrAdHoc()
	}
	if v.searchBarRect.contains(innerX, innerY) {
		v.searching = true
		v.searchInput.SetValue(v.searchQuery)
		v.searchInput.CursorEnd()
		return v.searchInput.Focus()
	}

	// Blur search if clicking elsewhere
	if v.searching {
		v.searching = false
		v.searchInput.Blur()
	}

	// Row 2 action toolbar pills
	for _, r := range v.toolbarRects {
		if r.contains(innerX, innerY) {
			switch r.id {
			case actNewHost:
				return v.openNewModal()
			case actQuickTerm:
				return v.openQuickTermModal()
			case actImport:
				return v.importSSHConfig()
			}
			return nil
		}
	}

	// Card hits
	for _, r := range v.cardRects {
		if r.contains(innerX, innerY) {
			if r.id >= 0 && r.id < len(v.filtered) {
				v.cursor = r.id
				if doubleClick {
					return v.connectSelected()
				}
			}
			return nil
		}
	}

	// Right inspector controls (⋯ menu, ⇥ collapse, full-width Connect)
	for _, r := range v.actionRects {
		if r.contains(innerX, innerY) {
			switch r.id {
			case actConnect:
				return v.connectSelected()
			case actMenu:
				if _, ok := v.selected(); ok {
					v.openContextMenuAt(r.x0-22, r.y1)
				}
				return nil
			case actCollapse:
				return func() tea.Msg { return ToggleInspectorMsg{} }
			}
			return nil
		}
	}
	return nil
}

func (v *HostsView) openContextMenuForSelected() {
	for _, r := range v.cardRects {
		if r.id == v.cursor {
			v.openContextMenuAt(r.x0+4, r.y1-1)
			return
		}
	}
	v.openContextMenuAt(4, 7)
}

func (v *HostsView) openContextMenuAt(x, y int) {
	v.menuOpen = true
	v.menuCursor = 0
	v.menuAnchorX = x
	v.menuAnchorY = y
}

func (v *HostsView) updateContextMenu(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "m":
		v.menuOpen = false
		return nil
	case "up", "k":
		v.menuCursor = (v.menuCursor - 1 + len(hostMenuEntries)) % len(hostMenuEntries)
		return nil
	case "down", "j":
		v.menuCursor = (v.menuCursor + 1) % len(hostMenuEntries)
		return nil
	case "enter":
		actionID := hostMenuEntries[v.menuCursor].id
		v.menuOpen = false
		return v.executeMenuAction(actionID)
	case "s", "S":
		v.menuOpen = false
		return v.executeMenuAction(actSFTP)
	case "e":
		v.menuOpen = false
		return v.executeMenuAction(actEdit)
	case "c":
		v.menuOpen = false
		return v.executeMenuAction(actDuplicate)
	case "d":
		v.menuOpen = false
		return v.executeMenuAction(actDelete)
	}
	return nil
}

func (v *HostsView) executeMenuAction(actionID int) tea.Cmd {
	switch actionID {
	case actConnect:
		return v.connectSelected()
	case actSFTP:
		return v.openSFTPSelected()
	case actEdit:
		return v.openEditModal()
	case actDuplicate:
		return v.duplicateSelected()
	case actDelete:
		return v.promptDeleteSelected()
	}
	return nil
}

func (v *HostsView) promptDeleteSelected() tea.Cmd {
	if h, ok := v.selected(); ok {
		v.deleteTargetID = h.ID
		v.confirm.Open("delete-host", "Delete Host", fmt.Sprintf("Delete host %q (%s@%s)?\nLinked port forwards will also be removed.", h.Name, h.Username, h.Hostname), true)
	}
	return nil
}

func (v *HostsView) duplicateSelected() tea.Cmd {
	h, ok := v.selected()
	if !ok {
		return nil
	}
	base := strings.TrimSpace(h.Name) + " (Copy)"
	name := base
	for i := 2; ; i++ {
		taken := false
		for _, existing := range v.all {
			if strings.EqualFold(existing.Name, name) {
				taken = true
				break
			}
		}
		if !taken {
			break
		}
		name = fmt.Sprintf("%s (Copy %d)", strings.TrimSpace(h.Name), i)
	}

	dup := &db.Host{
		Name:            name,
		Hostname:        h.Hostname,
		Port:            h.Port,
		Username:        h.Username,
		OSType:          h.OSType,
		GroupName:       h.GroupName,
		Tags:            h.Tags,
		KeychainID:      h.KeychainID,
		InlineSecretEnc: append([]byte(nil), h.InlineSecretEnc...),
		RecordSession:   h.RecordSession,
		Notes:           h.Notes,
	}
	if err := v.store.CreateHost(dup); err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Duplicate failed: %v", err))
	}
	_ = v.Reload()
	for i, item := range v.filtered {
		if item.ID == dup.ID {
			v.cursor = i
			break
		}
	}
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, fmt.Sprintf("Duplicated %q", dup.Name)),
	)
}

func (v *HostsView) moveCursor(delta int) {
	if len(v.filtered) == 0 {
		v.cursor = 0
		return
	}
	next := v.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(v.filtered) {
		next = len(v.filtered) - 1
	}
	v.cursor = next
}

func (v *HostsView) resolveSelectedWithKeychain() (db.Host, *db.KeychainItem, error) {
	h, ok := v.selected()
	if !ok {
		return db.Host{}, nil, fmt.Errorf("no host selected")
	}
	var kc *db.KeychainItem
	if h.KeychainID != nil {
		item, err := v.store.GetKeychain(*h.KeychainID)
		if err != nil {
			return db.Host{}, nil, err
		}
		kc = item
	}
	return h, kc, nil
}

func (v *HostsView) connectSelectedOrAdHoc() tea.Cmd {
	if host, ok := parseAdHocTarget(v.searchQuery); ok {
		return func() tea.Msg { return OpenSessionMsg{Host: host} }
	}
	return v.connectSelected()
}

func (v *HostsView) connectSelected() tea.Cmd {
	if _, ok := v.selected(); !ok {
		return nil
	}
	h, kc, err := v.resolveSelectedWithKeychain()
	if err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Load keychain: %v", err))
	}
	return func() tea.Msg {
		return OpenSessionMsg{Host: h, Keychain: kc}
	}
}

func (v *HostsView) openSFTPSelected() tea.Cmd {
	if _, ok := v.selected(); !ok {
		return nil
	}
	h, kc, err := v.resolveSelectedWithKeychain()
	if err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Load keychain: %v", err))
	}
	return func() tea.Msg {
		return OpenSFTPMsg{Host: h, Keychain: kc}
	}
}

func (v *HostsView) executeDelete() tea.Cmd {
	id := v.deleteTargetID
	v.deleteTargetID = 0
	if err := v.store.DeleteHost(id); err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Delete host failed: %v", err))
	}
	_ = v.Reload()
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, "Host deleted"),
	)
}

func (v *HostsView) importSSHConfig() tea.Cmd {
	imported, err := ssh.ParseSSHConfig(v.sshConfigPath)
	if err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Import failed: %v", err))
	}
	if len(imported) == 0 {
		return toastCmd(components.ToastInfo, "No hosts found in ~/.ssh/config")
	}

	added := 0
	skipped := 0
	for _, ih := range imported {
		exists, err := v.store.HostExists(ih.Name, ih.Hostname, ih.Port, ih.Username)
		if err != nil {
			return toastCmd(components.ToastError, fmt.Sprintf("Import check failed: %v", err))
		}
		if exists {
			skipped++
			continue
		}

		var kcID *int64
		if ih.IdentityFile != "" {
			if st, statErr := os.Stat(ih.IdentityFile); statErr == nil && !st.IsDir() {
				id, kcErr := v.ensureKeychainForPath(ih.IdentityFile, ih.Name)
				if kcErr == nil && id > 0 {
					kcID = &id
				}
			}
		}

		h := &db.Host{
			Name:       ih.Name,
			Hostname:   ih.Hostname,
			Port:       ih.Port,
			Username:   ih.Username,
			OSType:     "linux",
			GroupName:  "Imported",
			KeychainID: kcID,
		}
		if err := v.store.CreateHost(h); err != nil {
			skipped++
			continue
		}
		added++
	}

	_ = v.Reload()
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, fmt.Sprintf("Imported %d host(s), skipped %d", added, skipped)),
	)
}

func (v *HostsView) ensureKeychainForPath(privPath, hintName string) (int64, error) {
	if existing, err := v.store.GetKeychainByPath(privPath); err == nil && existing != nil {
		return existing.ID, nil
	}

	baseName := strings.TrimSpace(hintName)
	if baseName == "" {
		baseName = filepath.Base(privPath)
	}
	baseName += "-key"
	name := baseName
	for suffix := 2; ; suffix++ {
		found, _ := v.store.GetKeychainByName(name)
		if found == nil {
			break
		}
		name = fmt.Sprintf("%s-%d", baseName, suffix)
	}

	pubKey, _ := ssh.ReadPublicKeyFromPrivate(privPath, "")
	item := &db.KeychainItem{
		Name:           name,
		Type:           db.KeychainSSHKey,
		PrivateKeyPath: privPath,
		PublicKey:      pubKey,
	}
	if err := v.store.CreateKeychain(item); err != nil {
		return 0, err
	}
	return item.ID, nil
}

func (v *HostsView) openQuickTermModal() tea.Cmd {
	v.quickTermOpen = true
	v.quickTermErr = ""
	v.quickTermInput.SetValue("")
	v.quickTermInput.Focus()
	return nil
}

func (v *HostsView) updateQuickTermModal(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.quickTermOpen = false
		v.quickTermInput.Blur()
		return nil
	case "enter":
		raw := strings.TrimSpace(v.quickTermInput.Value())
		host, ok := parseAdHocTarget(raw)
		if !ok {
			if raw == "" || strings.ContainsAny(raw, " \t") {
				v.quickTermErr = "Enter target as user@hostname or user@hostname:port."
				return nil
			}
			user := os.Getenv("USER")
			if user == "" {
				user = "root"
			}
			host = db.Host{Name: raw, Hostname: raw, Port: 22, Username: user, OSType: "linux"}
		}
		v.quickTermOpen = false
		v.quickTermInput.Blur()
		return func() tea.Msg { return OpenSessionMsg{Host: host} }
	}
	var cmd tea.Cmd
	v.quickTermInput, cmd = v.quickTermInput.Update(msg)
	return cmd
}

func parseAdHocTarget(raw string) (db.Host, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(raw, "@") || strings.ContainsAny(raw, " \t") {
		return db.Host{}, false
	}
	parts := strings.SplitN(raw, "@", 2)
	user := strings.TrimSpace(parts[0])
	hostPart := strings.TrimSpace(parts[1])
	if user == "" || hostPart == "" {
		return db.Host{}, false
	}
	hostname := hostPart
	port := 22
	if colonIdx := strings.LastIndex(hostPart, ":"); colonIdx > 0 {
		p, err := strconv.Atoi(hostPart[colonIdx+1:])
		if err == nil && p >= 1 && p <= 65535 {
			hostname = hostPart[:colonIdx]
			port = p
		}
	}
	if hostname == "" {
		return db.Host{}, false
	}
	return db.Host{
		Name:     user + "@" + hostname,
		Hostname: hostname,
		Port:     port,
		Username: user,
		OSType:   "linux",
	}, true
}

func (v *HostsView) openNewModal() tea.Cmd {
	kcs, _ := v.store.ListKeychain()
	v.keychains = kcs

	v.modalOpen = true
	v.editingID = 0
	v.existingInlineSecret = nil
	v.existingNotes = ""
	v.formFocus = 0
	v.formErr = ""
	v.recordSession = false
	v.osIdx = 0
	v.kcIdx = 0

	if len(v.keychains) > 0 {
		v.authMode = hostAuthKeychain
	} else {
		v.authMode = hostAuthPassword
	}

	v.inName.SetValue("")
	v.inHostname.SetValue("")
	v.inPort.SetValue("22")
	v.inUsername.SetValue("")
	v.inPassword.SetValue("")
	v.inPassword.Placeholder = "password"
	v.inKeyPath.SetValue("~/.ssh/id_ed25519")
	v.inGroup.SetValue("General")
	v.inTags.SetValue("")

	v.syncModalFocus()
	return nil
}

func (v *HostsView) openEditModal() tea.Cmd {
	h, ok := v.selected()
	if !ok {
		return nil
	}
	kcs, _ := v.store.ListKeychain()
	v.keychains = kcs

	v.modalOpen = true
	v.editingID = h.ID
	v.existingInlineSecret = append([]byte(nil), h.InlineSecretEnc...)
	v.existingNotes = h.Notes
	v.formFocus = 0
	v.formErr = ""
	v.recordSession = h.RecordSession
	v.osIdx = theme.OSIndex(h.OSType)
	v.kcIdx = 0

	v.inName.SetValue(h.Name)
	v.inHostname.SetValue(h.Hostname)
	v.inPort.SetValue(strconv.Itoa(h.Port))
	v.inUsername.SetValue(h.Username)
	v.inPassword.SetValue("")
	v.inKeyPath.SetValue("~/.ssh/id_ed25519")
	v.inGroup.SetValue(h.GroupName)
	v.inTags.SetValue(h.Tags)

	if h.KeychainID != nil {
		v.authMode = hostAuthKeychain
		for i, k := range v.keychains {
			if k.ID == *h.KeychainID {
				v.kcIdx = i
				break
			}
		}
	} else if len(h.InlineSecretEnc) > 0 {
		v.authMode = hostAuthPassword
		v.inPassword.Placeholder = "(leave blank to keep saved password)"
	} else {
		v.authMode = hostAuthAgent
		v.inPassword.Placeholder = "password"
	}

	v.syncModalFocus()
	return nil
}

func (v *HostsView) activeFormFields() []int {
	fields := []int{hfName, hfHostname, hfPort, hfUsername, hfOS, hfAuthMode}
	if v.authMode != hostAuthAgent {
		fields = append(fields, hfAuthDetail)
	}
	fields = append(fields, hfGroup, hfTags, hfRecord)
	return fields
}

func (v *HostsView) syncModalFocus() {
	inputs := []*textinput.Model{
		&v.inName, &v.inHostname, &v.inPort, &v.inUsername,
		&v.inPassword, &v.inKeyPath, &v.inGroup, &v.inTags,
	}
	for _, ti := range inputs {
		ti.Blur()
	}

	fields := v.activeFormFields()
	v.formFocus = clampInt(v.formFocus, 0, len(fields)-1)

	switch fields[v.formFocus] {
	case hfName:
		v.inName.Focus()
	case hfHostname:
		v.inHostname.Focus()
	case hfPort:
		v.inPort.Focus()
	case hfUsername:
		v.inUsername.Focus()
	case hfAuthDetail:
		if v.authMode == hostAuthPassword {
			v.inPassword.Focus()
		} else if v.authMode == hostAuthKeyPath {
			v.inKeyPath.Focus()
		}
	case hfGroup:
		v.inGroup.Focus()
	case hfTags:
		v.inTags.Focus()
	}
}

func (v *HostsView) updateModal(msg tea.KeyMsg) tea.Cmd {
	fields := v.activeFormFields()
	curField := fields[v.formFocus]

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

	switch curField {
	case hfOS:
		n := len(theme.OSOptions)
		switch msg.String() {
		case "left", "h":
			v.osIdx = (v.osIdx - 1 + n) % n
		case "right", "l", " ":
			v.osIdx = (v.osIdx + 1) % n
		}
		return nil
	case hfAuthMode:
		switch msg.String() {
		case "left", "h":
			v.authMode = (v.authMode + 3) % 4
			v.syncModalFocus()
		case "right", "l", " ":
			v.authMode = (v.authMode + 1) % 4
			v.syncModalFocus()
		}
		return nil
	case hfAuthDetail:
		if v.authMode == hostAuthKeychain {
			if len(v.keychains) > 0 {
				switch msg.String() {
				case "left", "h":
					v.kcIdx = (v.kcIdx - 1 + len(v.keychains)) % len(v.keychains)
				case "right", "l", " ":
					v.kcIdx = (v.kcIdx + 1) % len(v.keychains)
				}
			}
			return nil
		}
	case hfRecord:
		switch msg.String() {
		case "left", "right", "h", "l", " ":
			v.recordSession = !v.recordSession
		}
		return nil
	}

	var cmd tea.Cmd
	switch curField {
	case hfName:
		v.inName, cmd = v.inName.Update(msg)
	case hfHostname:
		v.inHostname, cmd = v.inHostname.Update(msg)
	case hfPort:
		v.inPort, cmd = v.inPort.Update(msg)
	case hfUsername:
		v.inUsername, cmd = v.inUsername.Update(msg)
	case hfAuthDetail:
		if v.authMode == hostAuthPassword {
			v.inPassword, cmd = v.inPassword.Update(msg)
		} else if v.authMode == hostAuthKeyPath {
			v.inKeyPath, cmd = v.inKeyPath.Update(msg)
		}
	case hfGroup:
		v.inGroup, cmd = v.inGroup.Update(msg)
	case hfTags:
		v.inTags, cmd = v.inTags.Update(msg)
	}
	return cmd
}

func (v *HostsView) saveModal() tea.Cmd {
	name := strings.TrimSpace(v.inName.Value())
	hostname := strings.TrimSpace(v.inHostname.Value())
	username := strings.TrimSpace(v.inUsername.Value())
	if name == "" {
		v.formErr = "Host Name / Label is required."
		return nil
	}
	if hostname == "" {
		v.formErr = "Host / IP address cannot be empty."
		return nil
	}
	if username == "" {
		v.formErr = "SSH Username is required."
		return nil
	}

	portStr := strings.TrimSpace(v.inPort.Value())
	port := 22
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p < 1 || p > 65535 {
			v.formErr = "Port must be between 1 and 65535."
			return nil
		}
		port = p
	}

	var kcID *int64
	var inlineSecret []byte

	switch v.authMode {
	case hostAuthKeychain:
		if len(v.keychains) == 0 {
			v.formErr = "Keychain is empty. Create a Keychain entry first or switch Auth Mode."
			return nil
		}
		id := v.keychains[v.kcIdx].ID
		kcID = &id

	case hostAuthPassword:
		pw := v.inPassword.Value()
		if pw != "" {
			enc, err := v.vault.Encrypt(pw)
			if err != nil {
				v.formErr = fmt.Sprintf("Encrypt password: %v", err)
				return nil
			}
			inlineSecret = enc
		} else if v.editingID > 0 && len(v.existingInlineSecret) > 0 {
			inlineSecret = v.existingInlineSecret
		} else {
			v.formErr = "Password is required for Password authentication."
			return nil
		}

	case hostAuthKeyPath:
		rawPath := strings.TrimSpace(v.inKeyPath.Value())
		if rawPath == "" {
			v.formErr = "Private key path is required."
			return nil
		}
		expanded := config.ExpandPath(rawPath)
		info, err := os.Stat(expanded)
		if err != nil || info.IsDir() {
			v.formErr = fmt.Sprintf("Key file not found: %s", expanded)
			return nil
		}
		id, err := v.ensureKeychainForPath(expanded, name)
		if err != nil {
			v.formErr = fmt.Sprintf("Register key in Keychain: %v", err)
			return nil
		}
		kcID = &id

	case hostAuthAgent:
	}

	osKey := "linux"
	if v.osIdx >= 0 && v.osIdx < len(theme.OSOptions) {
		osKey = theme.OSOptions[v.osIdx].Key
	}

	h := &db.Host{
		ID:              v.editingID,
		Name:            name,
		Hostname:        hostname,
		Port:            port,
		Username:        username,
		OSType:          osKey,
		GroupName:       strings.TrimSpace(v.inGroup.Value()),
		Tags:            strings.TrimSpace(v.inTags.Value()),
		KeychainID:      kcID,
		InlineSecretEnc: inlineSecret,
		RecordSession:   v.recordSession,
		Notes:           v.existingNotes,
	}

	var err error
	if v.editingID > 0 {
		err = v.store.UpdateHost(h)
	} else {
		err = v.store.CreateHost(h)
	}
	if err != nil {
		v.formErr = err.Error()
		return nil
	}

	v.modalOpen = false
	_ = v.Reload()

	msgText := "Host created"
	if v.editingID > 0 {
		msgText = "Host updated"
	}
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, msgText),
	)
}

// ViewMain renders the full Termius 3-layer Hosts workspace:
// Layer 0 Crust canvas (full-width Search Bar + Action Toolbar + Elevated Cards Grid)
// + 1px divider + Layer 1 Mantle Right Host Details Panel + Floating Context Dropdown.
func (v *HostsView) ViewMain(w, h int) string {
	v.cardRects = v.cardRects[:0]
	v.actionRects = v.actionRects[:0]
	v.toolbarRects = v.toolbarRects[:0]
	v.menuItemRects = v.menuItemRects[:0]
	v.menuRect = rect{}

	if w <= 0 || h <= 0 {
		return ""
	}

	rightW := 0
	dividerW := 0
	canvasW := w
	if v.showInspector && w >= 74 {
		rightW = clampInt(w/3, 34, 38)
		dividerW = 1
		canvasW = w - rightW - dividerW
	}

	canvasStr := v.renderMainCanvas(canvasW, h)

	var full string
	if rightW > 0 {
		divLine := theme.DividerStyle.Render("│")
		divRows := make([]string, h)
		for i := range divRows {
			divRows[i] = divLine
		}
		dividerCol := strings.Join(divRows, "\n")
		rightPanel := v.renderRightDetailsPanel(rightW, h, canvasW+dividerW)
		full = lipgloss.JoinHorizontal(lipgloss.Top, canvasStr, dividerCol, rightPanel)
	} else {
		full = canvasStr
	}

	if v.menuOpen {
		dropdown := v.renderContextDropdown(w, h)
		if dropdown != "" {
			full = components.OverlayAt(full, dropdown, v.menuRect.x0, v.menuRect.y0, w, h)
		}
	}

	return full
}

func (v *HostsView) renderMainCanvas(w, h int) string {
	innerW := maxInt(16, w-2) // 1 col padding left & right on Crust

	// Row 1: Full-width rounded Search & Quick Connect bar (3 rows tall: y=0..3)
	searchBarStr := v.renderFullSearchBar(innerW)

	// Row 2: Action Toolbar (1 row: y=3)
	toolbarStr := v.renderActionToolbar(innerW)

	// Row 4: a deliberate breathing row between the action toolbar and the
	// "Hosts (N)" heading. Without it "+ New Host" and "Hosts" sit on adjacent
	// lines and the hierarchy looks cramped.
	spacerLine := theme.SpaceBg(theme.Crust, innerW)

	// Row 5: Section Header "Hosts (N)" (1 row)
	countLabel := fmt.Sprintf("Hosts (%d)", len(v.filtered))
	secTitle := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Crust).Bold(true).Render(
		fmt.Sprintf("Hosts"),
	) + lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Crust).Render(
		fmt.Sprintf(" (%d)", len(v.filtered)),
	)
	_ = countLabel
	secPad := maxInt(0, innerW-lipgloss.Width(secTitle))
	secLine := secTitle + theme.SpaceBg(theme.Crust, secPad)

	// Remaining height for elevated Host Card Grid (y=6..h)
	gridH := maxInt(1, h-6)
	gridStr := v.renderCardGrid(innerW, gridH)

	// Compose row-by-row instead of using JoinVertical: lipgloss pads ragged
	// blocks with unstyled spaces, which would expose the terminal's default
	// gray background. Every row is explicitly padded on Crust here.
	componentLines := make([]string, 0, h)
	componentLines = append(componentLines, strings.Split(searchBarStr, "\n")...)
	componentLines = append(componentLines, strings.Split(toolbarStr, "\n")...)
	componentLines = append(componentLines, spacerLine)
	componentLines = append(componentLines, secLine)
	componentLines = append(componentLines, strings.Split(gridStr, "\n")...)

	outLines := make([]string, 0, h)
	for i := 0; i < h; i++ {
		var row string
		if i < len(componentLines) {
			row = componentLines[i]
		}
		row = components.Truncate(row, innerW)
		rw := lipgloss.Width(row)
		if rw < innerW {
			row += theme.SpaceBg(theme.Crust, innerW-rw)
		}
		fullLine := theme.SpaceBg(theme.Crust, 1) + row + theme.SpaceBg(theme.Crust, 1)
		outLines = append(outLines, fullLine)
	}
	return strings.Join(outLines, "\n")
}

func (v *HostsView) renderFullSearchBar(innerW int) string {
	contentW := maxInt(10, innerW-4) // border(2) + padding(2)
	connectLabel := "Connect"
	if _, isAdHoc := parseAdHocTarget(v.searchQuery); isAdHoc {
		connectLabel = "Connect SSH"
	}
	connectPill := theme.ActionBarPrimaryPill.Render(connectLabel)
	pillW := lipgloss.Width(connectPill)

	icon := lipgloss.NewStyle().Foreground(theme.Blue).Background(theme.Base).Render("󰍉 ")
	inputAvail := maxInt(6, contentW-lipgloss.Width(icon)-pillW-2)

	var leftPart string
	if v.searching {
		leftPart = icon + RenderCleanTextInput(&v.searchInput, inputAvail, theme.Base)
	} else if strings.TrimSpace(v.searchQuery) != "" {
		qText := components.Truncate(v.searchQuery, inputAvail)
		leftPart = icon + lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Render(qText)
	} else {
		ph := components.Truncate("Find a host or type user@hostname...", inputAvail)
		leftPart = icon + lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base).Render(ph)
	}

	lw := lipgloss.Width(leftPart)
	gap := maxInt(1, contentW-lw-pillW)
	barContent := leftPart + theme.SpaceBg(theme.Base, gap) + connectPill

	barStyle := theme.SearchBarBlurred
	if v.searching {
		barStyle = theme.SearchBarFocused
	}
	rendered := barStyle.Width(innerW - 2).MaxWidth(innerW).MaxHeight(3).Render(components.Truncate(barContent, contentW))

	// Hitboxes relative to HostsView(0,0) (note 1 col Crust left pad)
	v.searchBarRect = rect{x0: 1, y0: 0, x1: 1 + innerW - pillW - 3, y1: 3}
	v.searchConnectRect = rect{x0: 1 + innerW - pillW - 3, y0: 0, x1: 1 + innerW, y1: 3}
	return rendered
}

func (v *HostsView) renderActionToolbar(innerW int) string {
	bg := lipgloss.NewStyle().Background(theme.Crust)
	pNew := theme.ActionBarPill.Render(
		lipgloss.NewStyle().Foreground(theme.Blue).Background(theme.Base).Bold(true).Render("+ ") +
			lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Render("New Host"),
	)
	pTerm := theme.ActionBarPill.Render(
		lipgloss.NewStyle().Foreground(theme.Mauve).Background(theme.Base).Render("󰆍 ") +
			lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Render("Quick Terminal"),
	)
	impText := "Import ~/.ssh/config"
	if innerW < 60 {
		impText = "Import"
	}
	pImp := theme.ActionBarPill.Render(
		lipgloss.NewStyle().Foreground(theme.Green).Background(theme.Base).Render("󰋺 ") +
			lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Render(impText),
	)

	w1 := lipgloss.Width(pNew)
	w2 := lipgloss.Width(pTerm)
	w3 := lipgloss.Width(pImp)

	var sb strings.Builder
	curX := 1 // canvas left padding
	addPill := func(id int, pill string, pw int) {
		if curX-1+pw > innerW {
			return
		}
		sb.WriteString(pill)
		v.toolbarRects = append(v.toolbarRects, rect{id: id, x0: curX, y0: 3, x1: curX + pw, y1: 4})
		curX += pw
	}

	addPill(actNewHost, pNew, w1)
	if curX-1+2+w2 <= innerW {
		sb.WriteString(theme.SpaceBg(theme.Crust, 2))
		curX += 2
		addPill(actQuickTerm, pTerm, w2)
	}
	if curX-1+2+w3 <= innerW {
		sb.WriteString(theme.SpaceBg(theme.Crust, 2))
		curX += 2
		addPill(actImport, pImp, w3)
	}

	used := curX - 1
	if used < innerW {
		sb.WriteString(theme.SpaceBg(theme.Crust, innerW-used))
	}
	_ = bg
	return components.Truncate(sb.String(), innerW)
}

func (v *HostsView) renderCardGrid(gridW, gridH int) string {
	if len(v.filtered) == 0 {
		v.gridCols = 1
		emptyMsg := "No hosts yet. Click [+ New Host] or press [n] to add, or [I] to import ~/.ssh/config."
		if strings.TrimSpace(v.searchQuery) != "" || v.searching {
			emptyMsg = "No hosts match your search. Press [Enter] on user@host to quick-connect."
		}
		line := lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Crust).Render(
			components.Truncate(emptyMsg, maxInt(4, gridW)),
		)
		rows := make([]string, gridH)
		rows[0] = line + theme.SpaceBg(theme.Crust, maxInt(0, gridW-lipgloss.Width(line)))
		for i := 1; i < gridH; i++ {
			rows[i] = theme.SpaceBg(theme.Crust, gridW)
		}
		return strings.Join(rows, "\n")
	}

	gapX := 2
	cols := 1
	if gridW >= 66 {
		cols = (gridW + gapX) / (32 + gapX)
		if cols < 1 {
			cols = 1
		}
		if cols > 3 {
			cols = 3
		}
	}
	v.gridCols = cols

	var cardOuterW int
	if cols == 1 {
		gapX = 0
		cardOuterW = minInt(42, gridW)
	} else {
		cardOuterW = minInt(36, (gridW-(cols-1)*gapX)/cols)
	}
	if cardOuterW < 20 {
		cols = 1
		v.gridCols = 1
		gapX = 0
		cardOuterW = gridW
	}

	cardOuterH := 6 // border(2) + vertical padding(2) + 2 content lines
	compactPad := false
	if gridH < 6 {
		cardOuterH = maxInt(4, gridH)
		compactPad = true
	}
	// One blank row between card rows when the canvas can afford it, so cards
	// don't sit flush against each other.
	gapY := 0
	if gridH >= 12 && !compactPad {
		gapY = 1
	}
	cardInnerW := maxInt(8, cardOuterW-6) // border(2) + Padding(1, 2) => horizontal pad 4

	totalRows := (len(v.filtered) + cols - 1) / cols
	visibleRows := maxInt(1, gridH/(cardOuterH+gapY))
	cursorRow := v.cursor / cols
	v.rowOffset = EnsureVisible(cursorRow, v.rowOffset, visibleRows, totalRows)

	var gridLines []string
	curY := 5 // rows 0..2 search bar, row 3 toolbar, row 4 section header
	for r := 0; r < visibleRows; r++ {
		rowIdx := v.rowOffset + r
		if rowIdx >= totalRows {
			break
		}

		var rowCards [][]string
		curX := 1 // 1 col Crust left padding
		for c := 0; c < cols; c++ {
			idx := rowIdx*cols + c
			if idx >= len(v.filtered) {
				break
			}
			card := v.renderHostCard(v.filtered[idx], idx == v.cursor, cardOuterW, cardOuterH, cardInnerW, compactPad)
			rowCards = append(rowCards, strings.Split(card, "\n"))
			v.cardRects = append(v.cardRects, rect{
				id: idx,
				x0: curX,
				y0: curY,
				x1: curX + cardOuterW,
				y1: curY + cardOuterH,
			})
			curX += cardOuterW + gapX
		}

		// Join card rows line-by-line so every gap and the empty space after
		// a short final row is filled with the Crust canvas background rather
		// than an unstyled placeholder or default terminal background.
		for lineIdx := 0; lineIdx < cardOuterH; lineIdx++ {
			var lineB strings.Builder
			for c, cardLines := range rowCards {
				if c > 0 {
					lineB.WriteString(theme.SpaceBg(theme.Crust, gapX))
				}
				if lineIdx < len(cardLines) {
					lineB.WriteString(cardLines[lineIdx])
				}
			}
			line := lineB.String()
			if lw := lipgloss.Width(line); lw < gridW {
				line += theme.SpaceBg(theme.Crust, gridW-lw)
			}
			gridLines = append(gridLines, line)
		}
		for g := 0; g < gapY && r+1 < visibleRows; g++ {
			gridLines = append(gridLines, theme.SpaceBg(theme.Crust, gridW))
		}
		curY += cardOuterH + gapY
	}

	for len(gridLines) < gridH {
		gridLines = append(gridLines, theme.SpaceBg(theme.Crust, gridW))
	}
	if len(gridLines) > gridH {
		gridLines = gridLines[:gridH]
	}
	return strings.Join(gridLines, "\n")
}

func (v *HostsView) renderHostCard(h db.Host, selected bool, outerW, outerH, innerW int, compactPad bool) string {
	bg := theme.Base
	if selected {
		bg = theme.Surface0
	}

	osInfo := theme.LookupOS(h.OSType)
	iconStyled := lipgloss.NewStyle().Foreground(osInfo.Color).Background(bg).Bold(true).Render(osInfo.Icon)

	dot := ""
	dotW := 0
	if v.activeHostIDs[h.ID] {
		dot = theme.SpaceBg(bg, 1) + lipgloss.NewStyle().Foreground(theme.Green).Background(bg).Bold(true).Render("●")
		dotW = 2
	}

	iconColW := 3 // icon + 2 spaces
	textW := maxInt(6, innerW-iconColW)

	nameMax := maxInt(4, textW-dotW)
	nameStr := components.Truncate(h.Name, nameMax)
	nameStyled := lipgloss.NewStyle().Foreground(theme.Text).Background(bg).Bold(true).Render(nameStr)
	namePad := maxInt(0, textW-lipgloss.Width(nameStr)-dotW)
	line0 := iconStyled + theme.SpaceBg(bg, 2) + nameStyled + dot + theme.SpaceBg(bg, namePad)

	sub := fmt.Sprintf("ssh, %s@%s", h.Username, h.Hostname)
	if h.Port > 0 && h.Port != 22 {
		sub = fmt.Sprintf("ssh, %s@%s:%d", h.Username, h.Hostname, h.Port)
	}
	subTrunc := components.Truncate(sub, textW)
	subStyled := lipgloss.NewStyle().Foreground(theme.Subtext0).Background(bg).Render(subTrunc)
	subPad := maxInt(0, textW-lipgloss.Width(subTrunc))
	line1 := theme.SpaceBg(bg, iconColW) + subStyled + theme.SpaceBg(bg, subPad)

	body := line0 + "\n" + line1

	style := theme.CardNormal
	if selected {
		style = theme.CardSelected
	}
	if compactPad {
		style = style.Padding(0, 2)
	}
	return style.
		Width(outerW - 2).
		Height(outerH - 2).
		MaxWidth(outerW).
		MaxHeight(outerH).
		Render(body)
}

func (v *HostsView) renderRightDetailsPanel(outerW, outerH, startX int) string {
	innerW := maxInt(14, outerW-2) // 1 col Mantle padding left & right

	panelLine := func(s string) string {
		s = components.Truncate(s, innerW)
		sw := lipgloss.Width(s)
		return theme.SpaceBg(theme.Mantle, 1) + s + theme.SpaceBg(theme.Mantle, maxInt(0, innerW-sw)+1)
	}

	// Panel Header: "Host Details" on left, [ ⋯ ] and [ ⇥ ] on right
	hdrTitle := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Mantle).Bold(true).Render("Host Details")
	menuBtn := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Padding(0, 1).Render("⋯")
	collapseBtn := lipgloss.NewStyle().Foreground(theme.Subtext0).Background(theme.Base).Padding(0, 1).Render("⇥")

	mw := lipgloss.Width(menuBtn)
	cw := lipgloss.Width(collapseBtn)
	tw := lipgloss.Width(hdrTitle)
	hdrGap := maxInt(1, innerW-tw-mw-cw-1)
	hdrRow := hdrTitle + theme.SpaceBg(theme.Mantle, hdrGap) + menuBtn + theme.SpaceBg(theme.Mantle, 1) + collapseBtn

	// Record hitboxes for [ ⋯ ] and [ ⇥ ] at y=0, starting at startX+1
	btnMenuX0 := startX + 1 + tw + hdrGap
	btnColX0 := btnMenuX0 + mw + 1
	v.actionRects = append(v.actionRects,
		rect{id: actMenu, x0: btnMenuX0, y0: 0, x1: btnMenuX0 + mw, y1: 1},
		rect{id: actCollapse, x0: btnColX0, y0: 0, x1: btnColX0 + cw, y1: 1},
	)

	item, ok := v.selected()
	if !ok {
		rows := make([]string, outerH)
		rows[0] = panelLine(hdrRow)
		rows[1] = theme.SpaceBg(theme.Mantle, outerW)
		rows[2] = panelLine(lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Mantle).Render("Select a host card to inspect details."))
		for i := 3; i < outerH; i++ {
			rows[i] = theme.SpaceBg(theme.Mantle, outerW)
		}
		return strings.Join(rows, "\n")
	}

	// Build 3 structured Mini-Cards (#1e1e2e Base inside #181825 Mantle)
	cardInnerW := maxInt(10, innerW-4) // border(2) + padding(2)
	mcLbl := func(title string) string {
		return lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base).Bold(true).Render(title)
	}
	mcKV := func(k, val string) string {
		kl := lipgloss.NewStyle().Foreground(theme.Subtext0).Background(theme.Base).Render(k + ": ")
		rem := maxInt(2, cardInnerW-lipgloss.Width(kl))
		vl := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Render(components.Truncate(val, rem))
		pad := maxInt(0, cardInnerW-lipgloss.Width(kl)-lipgloss.Width(vl))
		return kl + vl + theme.SpaceBg(theme.Base, pad)
	}

	osInfo := theme.LookupOS(item.OSType)
	osIcon := lipgloss.NewStyle().Foreground(osInfo.Color).Background(theme.Base).Bold(true).Render(osInfo.Icon)
	portBadge := lipgloss.NewStyle().Foreground(theme.Blue).Background(theme.Surface0).Padding(0, 1).Render(fmt.Sprintf(":%d", item.Port))
	hostMax := maxInt(4, cardInnerW-lipgloss.Width(osInfo.Icon)-2-lipgloss.Width(portBadge))
	hostDisp := components.Truncate(item.Hostname, hostMax)
	addrLine := osIcon + theme.SpaceBg(theme.Base, 1) +
		lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Bold(true).Render(hostDisp) +
		theme.SpaceBg(theme.Base, maxInt(1, cardInnerW-lipgloss.Width(osInfo.Icon)-1-lipgloss.Width(hostDisp)-lipgloss.Width(portBadge))) +
		portBadge

	box1 := theme.MiniCard.Width(innerW - 2).MaxWidth(innerW).Render(
		mcLbl("ADDRESS") + theme.SpaceBg(theme.Base, maxInt(0, cardInnerW-7)) + "\n" + addrLine,
	)

	tagDisplay := "-"
	if strings.TrimSpace(item.Tags) != "" {
		tagDisplay = item.Tags
	}
	box2 := theme.MiniCard.Width(innerW - 2).MaxWidth(innerW).Render(
		mcLbl("GENERAL") + theme.SpaceBg(theme.Base, maxInt(0, cardInnerW-7)) + "\n" +
			mcKV("Name", item.Name) + "\n" +
			mcKV("Group", item.GroupName) + "\n" +
			mcKV("Tags", tagDisplay),
	)

	recStr := "OFF"
	if item.RecordSession {
		recStr = "ON"
	}
	box3Lines := []string{
		mcLbl("CREDENTIALS") + theme.SpaceBg(theme.Base, maxInt(0, cardInnerW-11)),
		mcKV("User", item.Username),
		mcKV("Auth", hostAuthSummary(item)),
		mcKV("Record", recStr+" · "+HumanTimeAgo(item.LastConnectedAt)),
	}
	if strings.TrimSpace(item.Notes) != "" {
		box3Lines = append(box3Lines, mcKV("Notes", item.Notes))
	}
	box3 := theme.MiniCard.Width(innerW - 2).MaxWidth(innerW).Render(strings.Join(box3Lines, "\n"))

	// Full-width Connect button pinned to bottom row (y = outerH - 1)
	connectBtn := theme.PrimaryConnectBtn.Width(innerW).Render("Connect")

	// Assemble lines ensuring Header (y=0) and Connect button (y=outerH-1) NEVER clip!
	var middle []string
	if outerH >= 24 {
		middle = append(middle, "")
		middle = append(middle, strings.Split(box1, "\n")...)
		middle = append(middle, strings.Split(box2, "\n")...)
		middle = append(middle, strings.Split(box3, "\n")...)
	} else {
		// Compact mode on short terminals: Address + compact Credentials
		middle = append(middle, strings.Split(box1, "\n")...)
		middle = append(middle, strings.Split(box3, "\n")...)
	}

	maxMiddle := maxInt(0, outerH-2)
	if len(middle) > maxMiddle {
		middle = middle[:maxMiddle]
	}

	out := make([]string, 0, outerH)
	out = append(out, panelLine(hdrRow))
	for _, ml := range middle {
		out = append(out, panelLine(ml))
	}
	for len(out) < outerH-1 {
		out = append(out, theme.SpaceBg(theme.Mantle, outerW))
	}
	connectRowY := len(out)
	out = append(out, panelLine(connectBtn))

	v.actionRects = append(v.actionRects, rect{
		id: actConnect,
		x0: startX + 1,
		y0: connectRowY,
		x1: startX + 1 + innerW,
		y1: connectRowY + 1,
	})
	return strings.Join(out, "\n")
}

func (v *HostsView) renderContextDropdown(totalW, totalH int) string {
	const menuW = 28
	const menuInnerW = menuW - 2 // border(2)

	rows := make([]string, 0, len(hostMenuEntries)+1)
	itemRowOffsets := make([]int, len(hostMenuEntries))

	for i, entry := range hostMenuEntries {
		if i == len(hostMenuEntries)-1 {
			rows = append(rows, lipgloss.NewStyle().Foreground(theme.Surface0).Background(theme.Base).Render(strings.Repeat("─", menuInnerW)))
		}
		itemRowOffsets[i] = len(rows)

		bg := theme.Base
		if i == v.menuCursor {
			bg = theme.Surface0
		}
		fg := theme.Text
		if entry.danger {
			fg = theme.Red
		}

		left := fmt.Sprintf(" %s %s", entry.icon, entry.label)
		right := entry.shortcut + " "
		gap := maxInt(1, menuInnerW-lipgloss.Width(left)-lipgloss.Width(right))
		line := lipgloss.NewStyle().Foreground(fg).Background(bg).Render(left) +
			theme.SpaceBg(bg, gap) +
			lipgloss.NewStyle().Foreground(theme.Overlay1).Background(bg).Render(right)
		rows = append(rows, components.Truncate(line, menuInnerW))
	}

	box := theme.DropdownBox.Width(menuInnerW).MaxWidth(menuW).Render(strings.Join(rows, "\n"))
	boxW := lipgloss.Width(box)
	boxH := lipgloss.Height(box)

	x := clampInt(v.menuAnchorX, 0, maxInt(0, totalW-boxW))
	y := clampInt(v.menuAnchorY, 0, maxInt(0, totalH-boxH))
	v.menuRect = rect{x0: x, y0: y, x1: x + boxW, y1: y + boxH}

	for i, entry := range hostMenuEntries {
		ry := y + 1 + itemRowOffsets[i]
		v.menuItemRects = append(v.menuItemRects, rect{
			id: entry.id,
			x0: x + 1,
			y0: ry,
			x1: x + boxW - 1,
			y1: ry + 1,
		})
	}
	return box
}

func (v *HostsView) ViewInspector(_, _ int) string {
	return ""
}

func (v *HostsView) ViewModal(fullW, fullH int) string {
	if v.confirm.Active {
		return v.confirm.View(fullW, fullH)
	}
	if v.quickTermOpen {
		w := minInt(48, maxInt(20, fullW-14))
		inputRow := RenderRoundedInputBox(&v.quickTermInput, w, true, theme.Mantle, theme.Base)
		body := lipgloss.NewStyle().Foreground(theme.Subtext0).Background(theme.Base).Render("Enter ad-hoc SSH destination (user@host[:port]):") +
			"\n\n" + inputRow
		if v.quickTermErr != "" {
			body += "\n\n" + lipgloss.NewStyle().Foreground(theme.Red).Background(theme.Base).Render("✖ "+v.quickTermErr)
		}
		footer := theme.ButtonActive.Render("Connect (Enter)") + theme.SpaceBg(theme.Base, 2) + theme.ButtonInactive.Render("Cancel (Esc)")
		return components.RenderModal(fullW, fullH, "Quick Terminal", body, footer, 60)
	}
	if !v.modalOpen {
		return ""
	}
	return v.renderHostModal(fullW, fullH)
}

func (v *HostsView) renderHostModal(fullW, fullH int) string {
	if fullW <= 0 || fullH <= 0 {
		return ""
	}

	outerW := minInt(64, fullW-2)
	if outerW < 26 {
		outerW = fullW
	}
	padX := 3
	if outerW < 36 {
		padX = 1
	}
	padY := 1
	if fullH < 16 {
		padY = 0
	}
	contentW := maxInt(14, outerW-2-padX*2)

	padMantle := func(s string, w int) string {
		if w <= 0 {
			return ""
		}
		clamped := components.Truncate(s, w)
		if rem := w - lipgloss.Width(clamped); rem > 0 {
			clamped += theme.SpaceBg(theme.Mantle, rem)
		}
		return clamped
	}

	fields := v.activeFormFields()
	focusedField := fields[v.formFocus]

	fieldLbl := func(fieldID int, text string, w int) string {
		fg := theme.Subtext0
		bold := false
		if focusedField == fieldID {
			fg = theme.Blue
			bold = true
		}
		st := lipgloss.NewStyle().Foreground(fg).Background(theme.Mantle).Bold(bold)
		return padMantle(st.Render(components.Truncate(text, w)), w)
	}

	joinColsMantle := func(left, right string, leftW, rightW, gap int) []string {
		lLines := strings.Split(left, "\n")
		rLines := strings.Split(right, "\n")
		n := maxInt(len(lLines), len(rLines))
		out := make([]string, n)
		gapStr := theme.SpaceBg(theme.Mantle, gap)
		for i := 0; i < n; i++ {
			var lRow, rRow string
			if i < len(lLines) {
				lRow = lLines[i]
			}
			if i < len(rLines) {
				rRow = rLines[i]
			}
			out[i] = padMantle(lRow, leftW) + gapStr + padMantle(rRow, rightW)
		}
		return out
	}

	type formBlock struct {
		fields []int
		lines  []string
	}
	var blocks []formBlock

	// Row 1: Name (full width)
	b1 := fieldLbl(hfName, "Name *", contentW) + "\n" +
		RenderRoundedInputBox(&v.inName, contentW, focusedField == hfName, theme.Base, theme.Mantle)
	blocks = append(blocks, formBlock{fields: []int{hfName}, lines: strings.Split(b1, "\n")})

	// Row 2: Host / IP (72%) + Port (28%)
	const colGap = 2
	if contentW >= 24 {
		usable := contentW - colGap
		hostW := (usable * 72) / 100
		portW := usable - hostW
		if portW < 9 {
			portW = minInt(9, usable-10)
			hostW = usable - portW
		}
		leftCol := fieldLbl(hfHostname, "Host / IP *", hostW) + "\n" +
			RenderRoundedInputBox(&v.inHostname, hostW, focusedField == hfHostname, theme.Base, theme.Mantle)
		rightCol := fieldLbl(hfPort, "Port", portW) + "\n" +
			RenderRoundedInputBox(&v.inPort, portW, focusedField == hfPort, theme.Base, theme.Mantle)
		blocks = append(blocks, formBlock{
			fields: []int{hfHostname, hfPort},
			lines:  joinColsMantle(leftCol, rightCol, hostW, portW, colGap),
		})
	} else {
		rowHost := fieldLbl(hfHostname, "Host / IP *", contentW) + "\n" +
			RenderRoundedInputBox(&v.inHostname, contentW, focusedField == hfHostname, theme.Base, theme.Mantle)
		rowPort := fieldLbl(hfPort, "Port", contentW) + "\n" +
			RenderRoundedInputBox(&v.inPort, contentW, focusedField == hfPort, theme.Base, theme.Mantle)
		blocks = append(blocks, formBlock{
			fields: []int{hfHostname, hfPort},
			lines:  strings.Split(rowHost+"\n"+rowPort, "\n"),
		})
	}

	// Row 3: Username (55%) + OS Icon (45%)
	osOpt := theme.OSOptions[clampInt(v.osIdx, 0, len(theme.OSOptions)-1)]
	arrowFg := theme.Subtext0
	if focusedField == hfOS {
		arrowFg = theme.Blue
	}
	renderOSInner := func(boxW int) string {
		innerW := maxInt(2, boxW-4)
		if innerW < 5 {
			return lipgloss.NewStyle().Foreground(osOpt.Color).Background(theme.Base).Bold(true).Render(osOpt.Icon)
		}
		la := lipgloss.NewStyle().Foreground(arrowFg).Background(theme.Base).Bold(true).Render("◀")
		ra := lipgloss.NewStyle().Foreground(arrowFg).Background(theme.Base).Bold(true).Render("▶")
		midW := maxInt(1, innerW-2)
		osIcon := lipgloss.NewStyle().Foreground(osOpt.Color).Background(theme.Base).Bold(true).Render(osOpt.Icon)
		lblAvail := maxInt(1, midW-lipgloss.Width(osIcon)-1)
		osLbl := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Bold(focusedField == hfOS).
			Render(components.Truncate(osOpt.Label, lblAvail))
		mid := osIcon + theme.SpaceBg(theme.Base, 1) + osLbl
		mw := lipgloss.Width(mid)
		lPad := maxInt(0, (midW-mw)/2)
		rPad := maxInt(0, midW-mw-lPad)
		return la + theme.SpaceBg(theme.Base, lPad) + mid + theme.SpaceBg(theme.Base, rPad) + ra
	}

	if contentW >= 24 {
		usable := contentW - colGap
		userW := (usable * 55) / 100
		osW := usable - userW
		leftCol := fieldLbl(hfUsername, "Username *", userW) + "\n" +
			RenderRoundedInputBox(&v.inUsername, userW, focusedField == hfUsername, theme.Base, theme.Mantle)
		rightCol := fieldLbl(hfOS, "OS Icon", osW) + "\n" +
			RenderRoundedValueBox(renderOSInner(osW), osW, focusedField == hfOS, theme.Base, theme.Mantle)
		blocks = append(blocks, formBlock{
			fields: []int{hfUsername, hfOS},
			lines:  joinColsMantle(leftCol, rightCol, userW, osW, colGap),
		})
	} else {
		rowUser := fieldLbl(hfUsername, "Username *", contentW) + "\n" +
			RenderRoundedInputBox(&v.inUsername, contentW, focusedField == hfUsername, theme.Base, theme.Mantle)
		rowOS := fieldLbl(hfOS, "OS Icon", contentW) + "\n" +
			RenderRoundedValueBox(renderOSInner(contentW), contentW, focusedField == hfOS, theme.Base, theme.Mantle)
		blocks = append(blocks, formBlock{
			fields: []int{hfUsername, hfOS},
			lines:  strings.Split(rowUser+"\n"+rowOS, "\n"),
		})
	}

	// Row 4: Auth Mode pill selector + dynamic bordered input below
	authLabels := []struct {
		mode  int
		label string
	}{
		{hostAuthPassword, "Password"},
		{hostAuthKeychain, "Keychain"},
		{hostAuthKeyPath, "SSH Key"},
		{hostAuthAgent, "Agent"},
	}
	pillGap := 2
	if contentW < 44 {
		pillGap = 1
	}
	var pills []string
	for _, al := range authLabels {
		st := lipgloss.NewStyle().Foreground(theme.Subtext0).Background(theme.Base).Padding(0, 1)
		if v.authMode == al.mode {
			if focusedField == hfAuthMode {
				st = lipgloss.NewStyle().Foreground(theme.Crust).Background(theme.Blue).Bold(true).Padding(0, 1)
			} else {
				st = lipgloss.NewStyle().Foreground(theme.Blue).Background(theme.Surface0).Bold(true).Padding(0, 1)
			}
		}
		pills = append(pills, st.Render(al.label))
	}
	pillRow := padMantle(strings.Join(pills, theme.SpaceBg(theme.Mantle, pillGap)), contentW)
	blankMantle := theme.SpaceBg(theme.Mantle, contentW)

	block4Lines := []string{
		fieldLbl(hfAuthMode, "Auth Mode", contentW),
		pillRow,
		blankMantle,
	}
	switch v.authMode {
	case hostAuthPassword:
		pwLabel := "Password *"
		if v.editingID > 0 && len(v.existingInlineSecret) > 0 {
			pwLabel = "Password (leave blank to keep saved)"
		}
		block4Lines = append(block4Lines, fieldLbl(hfAuthDetail, pwLabel, contentW))
		block4Lines = append(block4Lines, strings.Split(RenderRoundedInputBox(&v.inPassword, contentW, focusedField == hfAuthDetail, theme.Base, theme.Mantle), "\n")...)
	case hostAuthKeyPath:
		block4Lines = append(block4Lines, fieldLbl(hfAuthDetail, "Key Path *", contentW))
		block4Lines = append(block4Lines, strings.Split(RenderRoundedInputBox(&v.inKeyPath, contentW, focusedField == hfAuthDetail, theme.Base, theme.Mantle), "\n")...)
	case hostAuthKeychain:
		block4Lines = append(block4Lines, fieldLbl(hfAuthDetail, "Keychain Credential", contentW))
		kcInnerW := maxInt(2, contentW-4)
		var kcInner string
		if len(v.keychains) > 0 {
			k := v.keychains[clampInt(v.kcIdx, 0, len(v.keychains)-1)]
			kcArrowFg := theme.Subtext0
			if focusedField == hfAuthDetail {
				kcArrowFg = theme.Blue
			}
			la := lipgloss.NewStyle().Foreground(kcArrowFg).Background(theme.Base).Bold(true).Render("◀")
			ra := lipgloss.NewStyle().Foreground(kcArrowFg).Background(theme.Base).Bold(true).Render("▶")
			kcMidW := maxInt(1, kcInnerW-2)
			kcText := components.Truncate(fmt.Sprintf("%s (%s)", k.Name, k.Type), kcMidW)
			kcStyled := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).Bold(focusedField == hfAuthDetail).Render(kcText)
			lp := maxInt(0, (kcMidW-lipgloss.Width(kcStyled))/2)
			rp := maxInt(0, kcMidW-lipgloss.Width(kcStyled)-lp)
			kcInner = la + theme.SpaceBg(theme.Base, lp) + kcStyled + theme.SpaceBg(theme.Base, rp) + ra
		} else {
			msg := components.Truncate("(no keychain items — create one in Keychain)", kcInnerW)
			kcInner = lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Base).Render(msg)
		}
		block4Lines = append(block4Lines, strings.Split(RenderRoundedValueBox(kcInner, contentW, focusedField == hfAuthDetail, theme.Base, theme.Mantle), "\n")...)
	case hostAuthAgent:
		agentHint := components.Truncate("Uses local SSH agent (SSH_AUTH_SOCK) / default keys", contentW)
		block4Lines = append(block4Lines, padMantle(lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Mantle).Render(agentHint), contentW))
	}
	blocks = append(blocks, formBlock{fields: []int{hfAuthMode, hfAuthDetail}, lines: block4Lines})

	// Row 5: Group (50%) + Tags (50%)
	if contentW >= 24 {
		usable := contentW - colGap
		groupW := usable / 2
		tagsW := usable - groupW
		leftCol := fieldLbl(hfGroup, "Group", groupW) + "\n" +
			RenderRoundedInputBox(&v.inGroup, groupW, focusedField == hfGroup, theme.Base, theme.Mantle)
		rightCol := fieldLbl(hfTags, "Tags", tagsW) + "\n" +
			RenderRoundedInputBox(&v.inTags, tagsW, focusedField == hfTags, theme.Base, theme.Mantle)
		blocks = append(blocks, formBlock{
			fields: []int{hfGroup, hfTags},
			lines:  joinColsMantle(leftCol, rightCol, groupW, tagsW, colGap),
		})
	} else {
		rowGroup := fieldLbl(hfGroup, "Group", contentW) + "\n" +
			RenderRoundedInputBox(&v.inGroup, contentW, focusedField == hfGroup, theme.Base, theme.Mantle)
		rowTags := fieldLbl(hfTags, "Tags", contentW) + "\n" +
			RenderRoundedInputBox(&v.inTags, contentW, focusedField == hfTags, theme.Base, theme.Mantle)
		blocks = append(blocks, formBlock{
			fields: []int{hfGroup, hfTags},
			lines:  strings.Split(rowGroup+"\n"+rowTags, "\n"),
		})
	}

	// Row 6: Recording toggle
	recBg := theme.Mantle
	if focusedField == hfRecord {
		recBg = theme.Surface0
	}
	var recBox string
	if v.recordSession {
		recBox = lipgloss.NewStyle().Foreground(theme.Green).Background(recBg).Bold(true).Render("[✓]")
	} else {
		boxFg := theme.Overlay1
		if focusedField == hfRecord {
			boxFg = theme.Blue
		}
		recBox = lipgloss.NewStyle().Foreground(boxFg).Background(recBg).Render("[ ]")
	}
	recTxtFg := theme.Subtext0
	if focusedField == hfRecord || v.recordSession {
		recTxtFg = theme.Text
	}
	recPill := lipgloss.NewStyle().Background(recBg).Padding(0, 1).Render(
		recBox + lipgloss.NewStyle().Foreground(recTxtFg).Background(recBg).Bold(focusedField == hfRecord).Render(" Record session logs"),
	)
	blocks = append(blocks, formBlock{
		fields: []int{hfRecord},
		lines:  []string{padMantle(recPill, contentW)},
	})

	// Title
	title := "New SSH Host"
	if v.editingID > 0 {
		title = "Edit SSH Host"
	}
	titleLine := padMantle(lipgloss.NewStyle().Foreground(theme.Blue).Background(theme.Mantle).Bold(true).Render(components.Truncate(title, contentW)), contentW)

	// Footer (blank line + subtle divider + right-aligned clean buttons + centered muted hint)
	cancelText := "Cancel (Esc)"
	saveText := "Save Host (Ctrl+S)"
	btnPadX := 2
	if contentW < 42 {
		cancelText = "Cancel"
		saveText = "Save Host"
		btnPadX = 1
	}
	cancelBtn := lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Surface0).Padding(0, btnPadX).Render(cancelText)
	saveBtn := lipgloss.NewStyle().Foreground(theme.Crust).Background(theme.Blue).Bold(true).Padding(0, btnPadX).Render(saveText)
	btnPair := cancelBtn + theme.SpaceBg(theme.Mantle, 2) + saveBtn
	if lipgloss.Width(btnPair) > contentW {
		btnPair = components.Truncate(cancelBtn+theme.SpaceBg(theme.Mantle, 1)+saveBtn, contentW)
	}
	btnLine := theme.SpaceBg(theme.Mantle, maxInt(0, contentW-lipgloss.Width(btnPair))) + btnPair
	btnLine = padMantle(btnLine, contentW)

	hintRaw := "Tab / ↑↓ Navigate • ←/→ Select • Ctrl+S Save • Esc Cancel"
	if contentW < len([]rune(hintRaw)) {
		hintRaw = "Tab/↑↓ Navigate • ←/→ Select • Ctrl+S Save • Esc Cancel"
	}
	if contentW >= 63 {
		hintRaw = "Tab / ↑↓ Navigate  •  ←/→ Select  •  Ctrl+S Save  •  Esc Cancel"
	}
	hintClamped := components.Truncate(hintRaw, contentW)
	hintStyled := lipgloss.NewStyle().Foreground(theme.Overlay1).Background(theme.Mantle).Render(hintClamped)
	hw := lipgloss.Width(hintStyled)
	hLPad := maxInt(0, (contentW-hw)/2)
	hRPad := maxInt(0, contentW-hw-hLPad)
	hintLine := theme.SpaceBg(theme.Mantle, hLPad) + hintStyled + theme.SpaceBg(theme.Mantle, hRPad)

	divLine := lipgloss.NewStyle().Foreground(theme.Surface0).Background(theme.Mantle).Render(strings.Repeat("─", contentW))

	var chromeH int
	var footerLines []string
	if fullH >= 16 {
		footerLines = []string{blankMantle, divLine, btnLine, hintLine}
		chromeH = 2 + padY*2 + 2 + len(footerLines) // border(2) + padY(2) + title+blank(2) + footer(4) = 10
	} else {
		footerLines = []string{btnLine, hintLine}
		chromeH = 2 + padY*2 + 1 + len(footerLines)
	}

	errLines := 0
	if v.formErr != "" {
		errLines = 1
	}
	availBody := maxInt(1, fullH-chromeH-errLines)

	// Block-aware viewport scrolling so rounded input boxes stay intact.
	focusBlock := 0
	for bi, blk := range blocks {
		for _, fid := range blk.fields {
			if fid == focusedField {
				focusBlock = bi
				break
			}
		}
	}

	baseBodyLines := 0
	for _, blk := range blocks {
		baseBodyLines += len(blk.lines)
	}

	// Give each form group one blank row of breathing room when the terminal
	// can display every field with it. On shorter terminals we keep the compact
	// form so no field is hidden from the user.
	formGap := 0
	if fullH >= 18 && len(blocks) > 1 && baseBodyLines+len(blocks)-1 <= availBody {
		formGap = 1
	}
	blockHeight := func(i int) int { return len(blocks[i].lines) + formGap }
	gapLine := theme.SpaceBg(theme.Mantle, contentW)

	totalBodyLines := 0
	for i := range blocks {
		totalBodyLines += blockHeight(i)
	}
	if totalBodyLines > 0 && formGap > 0 {
		totalBodyLines-- // no leading gap before the first block
	}

	var visibleLines []string
	if totalBodyLines <= availBody {
		for i, blk := range blocks {
			if i > 0 && formGap > 0 {
				visibleLines = append(visibleLines, gapLine)
			}
			visibleLines = append(visibleLines, blk.lines...)
		}
	} else {
		lo, hi := focusBlock, focusBlock
		used := blockHeight(focusBlock)
		for {
			expanded := false
			if hi+1 < len(blocks) && used+blockHeight(hi+1) <= availBody {
				hi++
				used += blockHeight(hi)
				expanded = true
			}
			if lo-1 >= 0 && used+blockHeight(lo-1) <= availBody {
				lo--
				used += blockHeight(lo)
				expanded = true
			}
			if !expanded {
				break
			}
		}
		for bi := lo; bi <= hi; bi++ {
			if bi > lo && formGap > 0 {
				visibleLines = append(visibleLines, gapLine)
			}
			visibleLines = append(visibleLines, blocks[bi].lines...)
		}
		if len(visibleLines) > availBody {
			visibleLines = visibleLines[:availBody]
		}
	}

	var allRows []string
	allRows = append(allRows, titleLine)
	if fullH >= 16 {
		allRows = append(allRows, blankMantle)
	}
	allRows = append(allRows, visibleLines...)
	if v.formErr != "" {
		errRow := padMantle(lipgloss.NewStyle().Foreground(theme.Red).Background(theme.Mantle).Bold(true).
			Render(components.Truncate("✖ "+v.formErr, contentW)), contentW)
		allRows = append(allRows, errRow)
	}
	allRows = append(allRows, footerLines...)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Surface1).
		BorderBackground(theme.Crust).
		Background(theme.Mantle).
		Foreground(theme.Text).
		Padding(padY, padX).
		Width(outerW - 2).
		MaxWidth(fullW).
		MaxHeight(fullH).
		Render(strings.Join(allRows, "\n"))

	return lipgloss.Place(
		fullW, fullH,
		lipgloss.Center, lipgloss.Center,
		box,
		lipgloss.WithWhitespaceBackground(theme.Crust),
	)
}

func (v *HostsView) Hints() []components.KeyHint {
	if v.searching {
		return []components.KeyHint{
			{Key: "Enter", Desc: "Filter / Connect user@host"},
			{Key: "Esc", Desc: "Clear"},
		}
	}
	return []components.KeyHint{
		{Key: "Enter", Desc: "Connect"},
		{Key: "m", Desc: "Menu"},
		{Key: "s", Desc: "SFTP"},
		{Key: "n", Desc: "New"},
		{Key: "t", Desc: "Quick Term"},
		{Key: "e", Desc: "Edit"},
		{Key: "I", Desc: "Import"},
		{Key: "/", Desc: "Search"},
		{Key: "i", Desc: "Detail"},
	}
}

func hostAuthSummary(h db.Host) string {
	if h.KeychainID != nil {
		if h.KeychainName != "" {
			if h.KeychainType == db.KeychainSSHKey {
				return "Key: " + h.KeychainName
			}
			return "KC: " + h.KeychainName
		}
		return "Keychain"
	}
	if len(h.InlineSecretEnc) > 0 {
		return "Password (Enc)"
	}
	return "Agent / Default"
}

func toastCmd(level components.ToastLevel, text string) tea.Cmd {
	return func() tea.Msg {
		return ToastMsg{Level: level, Text: text}
	}
}
