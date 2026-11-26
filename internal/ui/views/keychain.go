package views

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ib-jpg/ez-ssh/internal/clipboard"
	"github.com/ib-jpg/ez-ssh/internal/config"
	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
	"github.com/ib-jpg/ez-ssh/internal/ssh"
	"github.com/ib-jpg/ez-ssh/internal/ui/components"
	"github.com/ib-jpg/ez-ssh/internal/ui/theme"
)

const (
	kfName = iota
	kfType
	kfUsername
	kfPrivPath
	kfSecret
)

const (
	gfName = iota
	gfAlgo
	gfFilename
	gfPassphrase
)

// KeychainView implements Modul 2: encrypted credentials, SSH key generation,
// public key clipboard copy, and ssh-copy-id deployment.
type KeychainView struct {
	store  *db.Store
	vault  *crypto.Vault
	sshDir string

	all      []db.KeychainItem
	filtered []db.KeychainItem

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
	modalOpen      bool
	editingID      int64
	existingSecret []byte
	formFocus      int
	itemType       string
	formErr        string
	inName         textinput.Model
	inUsername     textinput.Model
	inPrivPath     textinput.Model
	inSecret       textinput.Model

	// Generate SSH Key modal
	genOpen       bool
	generating    bool
	genSpinner    spinner.Model
	genFocus      int
	genAlgo       ssh.KeyAlgorithm
	genFileEdited bool
	genErr        string
	inGenName     textinput.Model
	inGenFilename textinput.Model
	inGenPass     textinput.Model

	// Host picker for ssh-copy-id
	pickHostOpen bool
	pickHosts    []db.Host
	pickCursor   int
	pickKeyItem  db.KeychainItem
}

// NewKeychainView creates a KeychainView backed by store and vault.
func NewKeychainView(store *db.Store, vault *crypto.Vault, sshDir string) *KeychainView {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(theme.Mauve).Background(theme.Base)

	v := &KeychainView{
		store:         store,
		vault:         vault,
		sshDir:        sshDir,
		searchInput:   NewStyledInput("Search keychain...", 64),
		inName:        NewStyledInput("prod-deploy-key", 64),
		inUsername:    NewStyledInput("ubuntu (optional for key)", 64),
		inPrivPath:    NewStyledInput("~/.ssh/id_ed25519", 256),
		inSecret:      NewStyledInput("password or passphrase", 256),
		genSpinner:    sp,
		genAlgo:       ssh.AlgoEd25519,
		inGenName:     NewStyledInput("prod-ed25519", 64),
		inGenFilename: NewStyledInput("id_ed25519_prod", 128),
		inGenPass:     NewStyledInput("optional passphrase", 256),
	}
	v.inSecret.EchoMode = textinput.EchoPassword
	v.inSecret.EchoCharacter = '•'
	v.inGenPass.EchoMode = textinput.EchoPassword
	v.inGenPass.EchoCharacter = '•'
	_ = v.Reload()
	return v
}

func (v *KeychainView) Init() tea.Cmd { return nil }

func (v *KeychainView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

func (v *KeychainView) IsModalOpen() bool {
	return v.modalOpen || v.genOpen || v.pickHostOpen || v.confirm.Active
}

func (v *KeychainView) IsSearching() bool {
	return v.searching
}

func (v *KeychainView) Reload() error {
	items, err := v.store.ListKeychain()
	if err != nil {
		return err
	}
	v.all = items
	v.applyFilter()
	return nil
}

func (v *KeychainView) applyFilter() {
	q := v.searchQuery
	if v.searching {
		q = v.searchInput.Value()
	}
	v.filtered = v.filtered[:0]
	for _, it := range v.all {
		if components.FuzzyMatch(q, it.Name, it.Type, it.Username, it.PrivateKeyPath) {
			v.filtered = append(v.filtered, it)
		}
	}
	if len(v.filtered) == 0 {
		v.cursor = 0
		v.offset = 0
	} else if v.cursor >= len(v.filtered) {
		v.cursor = len(v.filtered) - 1
	}
}

func (v *KeychainView) selected() (db.KeychainItem, bool) {
	if len(v.filtered) == 0 || v.cursor < 0 || v.cursor >= len(v.filtered) {
		return db.KeychainItem{}, false
	}
	return v.filtered[v.cursor], true
}

func (v *KeychainView) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case KeyGenResultMsg:
		v.generating = false
		if m.Err != nil {
			v.genErr = m.Err.Error()
			return nil
		}
		item := m.Item
		if err := v.store.CreateKeychain(&item); err != nil {
			v.genErr = fmt.Sprintf("Key generated at %s, DB save failed: %v", m.PrivPath, err)
			return nil
		}
		v.genOpen = false
		_ = v.Reload()
		return tea.Batch(
			func() tea.Msg { return DataChangedMsg{} },
			toastCmd(components.ToastSuccess, fmt.Sprintf("Generated key %s", m.PrivPath)),
		)

	case spinner.TickMsg:
		if v.generating {
			var cmd tea.Cmd
			v.genSpinner, cmd = v.genSpinner.Update(m)
			return cmd
		}
		return nil

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
		if v.pickHostOpen {
			return v.updateHostPicker(m)
		}
		if v.genOpen {
			return v.updateGenModal(m)
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
		case "g":
			return v.openGenModal()
		case "c":
			return v.copyPublicKey()
		case "s":
			return v.openCopyIDPicker()
		case "d":
			if it, ok := v.selected(); ok {
				v.deleteTargetID = it.ID
				v.confirm.Open("delete-kc", "Delete Keychain Entry", fmt.Sprintf("Delete credential %q?\nHosts referencing it will fall back to default auth.", it.Name), true)
			}
		}
	}
	return nil
}

func (v *KeychainView) HandleClick(innerX, innerY int, doubleClick bool) tea.Cmd {
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

func (v *KeychainView) moveCursor(delta int) {
	if len(v.filtered) == 0 {
		v.cursor = 0
		return
	}
	v.cursor = clampInt(v.cursor+delta, 0, len(v.filtered)-1)
}

func (v *KeychainView) executeDelete() tea.Cmd {
	id := v.deleteTargetID
	v.deleteTargetID = 0
	if err := v.store.DeleteKeychain(id); err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("Delete failed: %v", err))
	}
	_ = v.Reload()
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, "Keychain entry deleted"),
	)
}

func (v *KeychainView) copyPublicKey() tea.Cmd {
	it, ok := v.selected()
	if !ok {
		return nil
	}
	if it.Type != db.KeychainSSHKey {
		return toastCmd(components.ToastInfo, "Clipboard copy is for SSH public keys (passwords stay encrypted)")
	}
	pub := strings.TrimSpace(it.PublicKey)
	if pub == "" {
		pass := ""
		if len(it.SecretEnc) > 0 && v.vault != nil {
			pass, _ = v.vault.Decrypt(it.SecretEnc)
		}
		derived, err := ssh.ReadPublicKeyFromPrivate(config.ExpandPath(it.PrivateKeyPath), pass)
		if err != nil {
			return toastCmd(components.ToastError, fmt.Sprintf("Cannot read public key: %v", err))
		}
		pub = derived
		it.PublicKey = pub
		_ = v.store.UpdateKeychain(&it)
		_ = v.Reload()
	}

	method, err := clipboard.Write(pub)
	if err != nil {
		return toastCmd(components.ToastError, err.Error())
	}
	return toastCmd(components.ToastSuccess, fmt.Sprintf("Public key copied (%s)", method))
}

func (v *KeychainView) openCopyIDPicker() tea.Cmd {
	it, ok := v.selected()
	if !ok {
		return nil
	}
	if it.Type != db.KeychainSSHKey {
		return toastCmd(components.ToastInfo, "ssh-copy-id requires an SSH Key entry")
	}
	hosts, err := v.store.ListHosts()
	if err != nil {
		return toastCmd(components.ToastError, fmt.Sprintf("List hosts: %v", err))
	}
	if len(hosts) == 0 {
		return toastCmd(components.ToastInfo, "Create a Host first before running ssh-copy-id")
	}
	v.pickHostOpen = true
	v.pickHosts = hosts
	v.pickCursor = 0
	v.pickKeyItem = it
	return nil
}

func (v *KeychainView) updateHostPicker(msg tea.KeyMsg) tea.Cmd {
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
		target := v.pickHosts[v.pickCursor]
		v.pickHostOpen = false

		privPath := config.ExpandPath(v.pickKeyItem.PrivateKeyPath)
		pubPath := privPath + ".pub"
		if _, err := os.Stat(pubPath); os.IsNotExist(err) {
			if strings.TrimSpace(v.pickKeyItem.PublicKey) != "" {
				_ = os.WriteFile(pubPath, []byte(strings.TrimSpace(v.pickKeyItem.PublicKey)+"\n"), 0o644)
			} else {
				pubPath = privPath
			}
		}

		var hostKC *db.KeychainItem
		if target.KeychainID != nil {
			hostKC, _ = v.store.GetKeychain(*target.KeychainID)
		}

		return func() tea.Msg {
			return ExecCopyIDMsg{
				PubKeyPath:   pubPath,
				Host:         target,
				HostKeychain: hostKC,
			}
		}
	}
	return nil
}

func (v *KeychainView) openNewModal() tea.Cmd {
	v.modalOpen = true
	v.editingID = 0
	v.existingSecret = nil
	v.itemType = db.KeychainPassword
	v.formFocus = 0
	v.formErr = ""
	v.inName.SetValue("")
	v.inUsername.SetValue("")
	v.inPrivPath.SetValue("~/.ssh/id_ed25519")
	v.inSecret.SetValue("")
	v.inSecret.Placeholder = "password or passphrase"
	v.syncModalFocus()
	return nil
}

func (v *KeychainView) openEditModal() tea.Cmd {
	it, ok := v.selected()
	if !ok {
		return nil
	}
	v.modalOpen = true
	v.editingID = it.ID
	v.existingSecret = append([]byte(nil), it.SecretEnc...)
	v.itemType = it.Type
	v.formFocus = 0
	v.formErr = ""
	v.inName.SetValue(it.Name)
	v.inUsername.SetValue(it.Username)
	v.inPrivPath.SetValue(it.PrivateKeyPath)
	v.inSecret.SetValue("")
	if len(it.SecretEnc) > 0 {
		v.inSecret.Placeholder = "(leave blank to keep existing)"
	} else {
		v.inSecret.Placeholder = "password or passphrase (optional)"
	}
	v.syncModalFocus()
	return nil
}

func (v *KeychainView) activeFormFields() []int {
	if v.itemType == db.KeychainSSHKey {
		return []int{kfName, kfType, kfUsername, kfPrivPath, kfSecret}
	}
	return []int{kfName, kfType, kfUsername, kfSecret}
}

func (v *KeychainView) syncModalFocus() {
	for _, ti := range []*textinput.Model{&v.inName, &v.inUsername, &v.inPrivPath, &v.inSecret} {
		ti.Blur()
	}
	fields := v.activeFormFields()
	v.formFocus = clampInt(v.formFocus, 0, len(fields)-1)
	switch fields[v.formFocus] {
	case kfName:
		v.inName.Focus()
	case kfUsername:
		v.inUsername.Focus()
	case kfPrivPath:
		v.inPrivPath.Focus()
	case kfSecret:
		v.inSecret.Focus()
	}
}

func (v *KeychainView) updateModal(msg tea.KeyMsg) tea.Cmd {
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

	if cur == kfType {
		switch msg.String() {
		case "left", "right", "h", "l", " ":
			if v.itemType == db.KeychainPassword {
				v.itemType = db.KeychainSSHKey
			} else {
				v.itemType = db.KeychainPassword
			}
			v.syncModalFocus()
		}
		return nil
	}

	var cmd tea.Cmd
	switch cur {
	case kfName:
		v.inName, cmd = v.inName.Update(msg)
	case kfUsername:
		v.inUsername, cmd = v.inUsername.Update(msg)
	case kfPrivPath:
		v.inPrivPath, cmd = v.inPrivPath.Update(msg)
	case kfSecret:
		v.inSecret, cmd = v.inSecret.Update(msg)
	}
	return cmd
}

func (v *KeychainView) saveModal() tea.Cmd {
	name := strings.TrimSpace(v.inName.Value())
	if name == "" {
		v.formErr = "Credential name is required."
		return nil
	}

	var secretEnc []byte
	rawSecret := v.inSecret.Value()
	if rawSecret != "" {
		enc, err := v.vault.Encrypt(rawSecret)
		if err != nil {
			v.formErr = fmt.Sprintf("Encrypt secret: %v", err)
			return nil
		}
		secretEnc = enc
	} else if v.editingID > 0 && len(v.existingSecret) > 0 {
		secretEnc = v.existingSecret
	}

	item := &db.KeychainItem{
		ID:        v.editingID,
		Name:      name,
		Type:      v.itemType,
		Username:  strings.TrimSpace(v.inUsername.Value()),
		SecretEnc: secretEnc,
	}

	if v.itemType == db.KeychainSSHKey {
		expanded := config.ExpandPath(strings.TrimSpace(v.inPrivPath.Value()))
		if expanded == "" {
			v.formErr = "Private key path is required."
			return nil
		}
		info, err := os.Stat(expanded)
		if err != nil || info.IsDir() {
			v.formErr = fmt.Sprintf("Private key file not found: %s", expanded)
			return nil
		}
		item.PrivateKeyPath = expanded

		passForPub := rawSecret
		if passForPub == "" && len(secretEnc) > 0 {
			passForPub, _ = v.vault.Decrypt(secretEnc)
		}
		if pub, err := ssh.ReadPublicKeyFromPrivate(expanded, passForPub); err == nil {
			item.PublicKey = pub
		}
	} else if len(item.SecretEnc) == 0 {
		v.formErr = "Password is required for Password credentials."
		return nil
	}

	var err error
	if v.editingID > 0 {
		err = v.store.UpdateKeychain(item)
	} else {
		err = v.store.CreateKeychain(item)
	}
	if err != nil {
		v.formErr = err.Error()
		return nil
	}

	v.modalOpen = false
	_ = v.Reload()
	return tea.Batch(
		func() tea.Msg { return DataChangedMsg{} },
		toastCmd(components.ToastSuccess, "Keychain saved"),
	)
}

func (v *KeychainView) openGenModal() tea.Cmd {
	v.genOpen = true
	v.generating = false
	v.genFocus = 0
	v.genAlgo = ssh.AlgoEd25519
	v.genFileEdited = false
	v.genErr = ""
	v.inGenName.SetValue("")
	v.inGenFilename.SetValue("id_ed25519_key")
	v.inGenPass.SetValue("")
	v.syncGenFocus()
	return nil
}

func (v *KeychainView) syncGenFocus() {
	v.inGenName.Blur()
	v.inGenFilename.Blur()
	v.inGenPass.Blur()
	v.genFocus = clampInt(v.genFocus, 0, 3)
	switch v.genFocus {
	case gfName:
		v.inGenName.Focus()
	case gfFilename:
		v.inGenFilename.Focus()
	case gfPassphrase:
		v.inGenPass.Focus()
	}
}

func (v *KeychainView) updateGenModal(msg tea.KeyMsg) tea.Cmd {
	if v.generating {
		return nil
	}
	switch msg.String() {
	case "esc":
		v.genOpen = false
		return nil
	case "tab", "down":
		v.genFocus = (v.genFocus + 1) % 4
		v.syncGenFocus()
		return nil
	case "shift+tab", "up":
		v.genFocus = (v.genFocus + 3) % 4
		v.syncGenFocus()
		return nil
	case "ctrl+s", "enter":
		return v.startKeyGen()
	}

	if v.genFocus == gfAlgo {
		switch msg.String() {
		case "left", "right", "h", "l", " ":
			if v.genAlgo == ssh.AlgoEd25519 {
				v.genAlgo = ssh.AlgoRSA4096
			} else {
				v.genAlgo = ssh.AlgoEd25519
			}
			if !v.genFileEdited {
				v.updateDefaultGenFilename()
			}
		}
		return nil
	}

	var cmd tea.Cmd
	switch v.genFocus {
	case gfName:
		v.inGenName, cmd = v.inGenName.Update(msg)
		if !v.genFileEdited {
			v.updateDefaultGenFilename()
		}
	case gfFilename:
		v.genFileEdited = true
		v.inGenFilename, cmd = v.inGenFilename.Update(msg)
	case gfPassphrase:
		v.inGenPass, cmd = v.inGenPass.Update(msg)
	}
	return cmd
}

func (v *KeychainView) updateDefaultGenFilename() {
	prefix := "id_ed25519_"
	if v.genAlgo == ssh.AlgoRSA4096 {
		prefix = "id_rsa_"
	}
	suffix := strings.TrimSpace(v.inGenName.Value())
	if suffix == "" {
		suffix = "key"
	}
	suffix = strings.ReplaceAll(suffix, " ", "_")
	v.inGenFilename.SetValue(prefix + suffix)
}

func (v *KeychainView) startKeyGen() tea.Cmd {
	name := strings.TrimSpace(v.inGenName.Value())
	filename := strings.TrimSpace(v.inGenFilename.Value())
	pass := v.inGenPass.Value()
	if name == "" || filename == "" {
		v.genErr = "Keychain name and filename are required."
		return nil
	}
	if existing, _ := v.store.GetKeychainByName(name); existing != nil {
		v.genErr = fmt.Sprintf("Keychain entry %q already exists.", name)
		return nil
	}

	v.generating = true
	v.genErr = ""
	algo := v.genAlgo
	sshDir := v.sshDir
	vault := v.vault

	return tea.Batch(
		v.genSpinner.Tick,
		func() tea.Msg {
			privPath, _, pubStr, err := ssh.GenerateKey(sshDir, filename, name, algo, pass)
			if err != nil {
				return KeyGenResultMsg{Err: err}
			}
			var secEnc []byte
			if pass != "" {
				secEnc, err = vault.Encrypt(pass)
				if err != nil {
					return KeyGenResultMsg{Err: err}
				}
			}
			return KeyGenResultMsg{
				PrivPath: privPath,
				Item: db.KeychainItem{
					Name:           name,
					Type:           db.KeychainSSHKey,
					SecretEnc:      secEnc,
					PrivateKeyPath: privPath,
					PublicKey:      pubStr,
				},
			}
		},
	)
}

func (v *KeychainView) ViewMain(w, h int) string {
	hdr, layout := RenderWorkspaceHeader(w, v.searching, v.searchInput, v.searchQuery, "New", "")
	v.headerLayout = layout

	cols := []Column{
		{Title: "Name", Weight: 3, MinWidth: 10},
		{Title: "Type", Weight: 1, MinWidth: 8, MaxWidth: 12},
		{Title: "Identity / Path", Weight: 4, MinWidth: 14},
		{Title: "Created", Weight: 2, MinWidth: 10, MaxWidth: 16},
	}

	rows := make([][]string, len(v.filtered))
	for i, it := range v.filtered {
		typ := "Password"
		detail := it.Username
		if detail == "" {
			detail = "(encrypted)"
		}
		if it.Type == db.KeychainSSHKey {
			typ = "SSH Key"
			detail = it.PrivateKeyPath
		}
		created := "-"
		if !it.CreatedAt.IsZero() {
			created = it.CreatedAt.Local().Format("2006-01-02")
		}
		rows[i] = []string{it.Name, typ, detail, created}
	}

	emptyMsg := "Keychain is empty. Press [n] to add or [g] to generate an SSH key."
	if strings.TrimSpace(v.searchQuery) != "" || v.searching {
		emptyMsg = "No credentials match your search."
	}

	tw, th := TablePaneInnerSize(w, h)
	tableStr, newOff := RenderTable(tw, th, cols, rows, v.cursor, v.offset, emptyMsg)
	v.offset = newOff
	return RenderWorkspacePane(w, h, hdr, v.searching, tableStr)
}

func (v *KeychainView) ViewInspector(w, h int) string {
	it, ok := v.selected()
	if !ok {
		return RenderInspectorLines(w, h, "KEYCHAIN DETAIL", []string{theme.InspectorMuted.Render("Select a credential to inspect.")})
	}

	secStatus := theme.StatusStopped.Background(theme.Mantle).Render("None")
	if len(it.SecretEnc) > 0 {
		secStatus = theme.StatusActive.Background(theme.Mantle).Render("Encrypted (AES-256-GCM)")
	}

	lines := []string{
		FormatKV("Type", it.Type, w) + theme.SpaceBg(theme.Mantle, 3) + theme.InspectorLabel.Render("Secret: ") + secStatus,
	}
	if it.Username != "" {
		lines = append(lines, FormatKV("Username", it.Username, w))
	}
	if it.Type == db.KeychainSSHKey {
		lines = append(lines, FormatKV("Priv Key", it.PrivateKeyPath, w))
		pub := it.PublicKey
		if pub == "" {
			pub = "(press [c] to derive/copy)"
		}
		lines = append(lines, FormatKV("Pub Key", pub, w))
	} else {
		lines = append(lines, FormatKV("Storage", "Protected in local vault.db", w))
	}

	return RenderInspectorLines(w, h, "CREDENTIAL: "+it.Name, lines)
}

func (v *KeychainView) ViewModal(fullW, fullH int) string {
	if v.confirm.Active {
		return v.confirm.View(fullW, fullH)
	}
	if v.pickHostOpen {
		items := make([]string, len(v.pickHosts))
		for i, h := range v.pickHosts {
			items[i] = fmt.Sprintf("%s (%s@%s:%d)", h.Name, h.Username, h.Hostname, h.Port)
		}
		maxVis := clampInt(fullH-10, 4, 14)
		body := RenderPickerList(items, v.pickCursor, maxVis, 48)
		footer := theme.InputHint.Render("[j/k] Select Host  [Enter] Run ssh-copy-id  [Esc] Cancel")
		return components.RenderModal(fullW, fullH, "Deploy Public Key ("+v.pickKeyItem.Name+")", body, footer, 60)
	}
	if v.genOpen {
		if v.generating {
			body := v.genSpinner.View() + lipgloss.NewStyle().Foreground(theme.Text).Background(theme.Base).
				Render(" Generating "+string(v.genAlgo)+" keypair...")
			return components.RenderModal(fullW, fullH, "Generate SSH Key", body, "", 54)
		}
		algoLabel := "◄ Ed25519 (Recommended) ►"
		if v.genAlgo == ssh.AlgoRSA4096 {
			algoLabel = "◄ RSA 4096-bit ►"
		}
		inW := clampInt(fullW-28, 16, 40)
		rows := []string{
			v.renderGenRow(gfName, "Name*", RenderCleanTextInput(&v.inGenName, inW, theme.Base)),
			v.renderGenRow(gfAlgo, "Algorithm", theme.ActionPill.Render(algoLabel)),
			v.renderGenRow(gfFilename, "Filename*", RenderCleanTextInput(&v.inGenFilename, inW, theme.Base)),
			v.renderGenRow(gfPassphrase, "Passphrase", RenderCleanTextInput(&v.inGenPass, inW, theme.Base)),
			theme.InputHint.Render("Saved to: " + filepath.Join(v.sshDir, strings.TrimSpace(v.inGenFilename.Value()))),
		}
		if v.genErr != "" {
			rows = append(rows, "", theme.StatusError.Render("✖ "+v.genErr))
		}
		footer := theme.InputHint.Render("[Tab/↑↓] Field  [←/→] Algorithm  [Enter] Generate  [Esc] Cancel")
		return components.RenderModal(fullW, fullH, "Generate New SSH Key", strings.Join(rows, "\n"), footer, 66)
	}
	if !v.modalOpen {
		return ""
	}

	title := "New Keychain Credential"
	if v.editingID > 0 {
		title = "Edit Keychain Credential"
	}

	inW := clampInt(fullW-28, 16, 40)
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
		case kfName:
			rows = append(rows, lbl("Name*")+RenderCleanTextInput(&v.inName, inW, theme.Base))
		case kfType:
			tLabel := "Password / Identity"
			if v.itemType == db.KeychainSSHKey {
				tLabel = "SSH Key (Import)"
			}
			rows = append(rows, lbl("Type")+theme.ActionPill.Render("◄ "+tLabel+" ►"))
		case kfUsername:
			rows = append(rows, lbl("Username")+RenderCleanTextInput(&v.inUsername, inW, theme.Base))
		case kfPrivPath:
			rows = append(rows, lbl("Key Path*")+RenderCleanTextInput(&v.inPrivPath, inW, theme.Base))
		case kfSecret:
			label := "Password*"
			if v.itemType == db.KeychainSSHKey {
				label = "Passphrase"
			}
			rows = append(rows, lbl(label)+RenderCleanTextInput(&v.inSecret, inW, theme.Base))
		}
	}
	if v.formErr != "" {
		rows = append(rows, "", theme.StatusError.Render("✖ "+v.formErr))
	}
	footer := theme.InputHint.Render("[Tab/↑↓] Field  [←/→] Type  [Enter] Save  [Esc] Cancel")
	return components.RenderModal(fullW, fullH, title, strings.Join(rows, "\n"), footer, 64)
}

func (v *KeychainView) renderGenRow(idx int, label, value string) string {
	p := components.PadRight(label, 13)
	if v.genFocus == idx {
		return theme.InputLabel.Render("▸ "+p) + value
	}
	return theme.InspectorLabel.Background(theme.Base).Render("  "+p) + value
}

func (v *KeychainView) Hints() []components.KeyHint {
	if v.searching {
		return []components.KeyHint{{Key: "Enter", Desc: "Apply"}, {Key: "Esc", Desc: "Clear"}}
	}
	return []components.KeyHint{
		{Key: "n", Desc: "New"},
		{Key: "g", Desc: "Gen Key"},
		{Key: "e", Desc: "Edit"},
		{Key: "c", Desc: "Copy PubKey"},
		{Key: "s", Desc: "ssh-copy-id"},
		{Key: "d", Desc: "Delete"},
		{Key: "/", Desc: "Search"},
		{Key: "q", Desc: "Quit"},
	}
}
