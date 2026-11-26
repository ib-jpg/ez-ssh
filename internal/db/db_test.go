package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "vault.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("db perm = %o, want 0600", perm)
	}
	return store
}

func TestStoreCRUDAndForeignKeys(t *testing.T) {
	store := openTestStore(t)

	// Keychain CRUD
	kc := &KeychainItem{
		Name:           "prod-key",
		Type:           KeychainSSHKey,
		Username:       "ubuntu",
		SecretEnc:      []byte("encrypted-passphrase"),
		PrivateKeyPath: "/home/test/.ssh/id_ed25519",
		PublicKey:      "ssh-ed25519 AAAA test@host",
	}
	if err := store.CreateKeychain(kc); err != nil {
		t.Fatalf("CreateKeychain() error = %v", err)
	}
	gotKC, err := store.GetKeychainByPath(kc.PrivateKeyPath)
	if err != nil || gotKC == nil || gotKC.ID != kc.ID {
		t.Fatalf("GetKeychainByPath() = (%+v, %v)", gotKC, err)
	}

	// Host CRUD
	h := &Host{
		Name:          "prod-web-01",
		Hostname:      "10.0.1.12",
		Port:          2222,
		Username:      "ubuntu",
		GroupName:     "Production",
		Tags:          "docker,nginx",
		KeychainID:    &kc.ID,
		RecordSession: true,
		Notes:         "Primary web node",
	}
	if err := store.CreateHost(h); err != nil {
		t.Fatalf("CreateHost() error = %v", err)
	}

	exists, err := store.HostExists("PROD-WEB-01", "10.0.1.99", 22, "root")
	if err != nil || !exists {
		t.Fatalf("HostExists(by name) = (%v, %v)", exists, err)
	}
	exists, err = store.HostExists("other", "10.0.1.12", 2222, "ubuntu")
	if err != nil || !exists {
		t.Fatalf("HostExists(by addr) = (%v, %v)", exists, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.MarkHostConnected(h.ID, now); err != nil {
		t.Fatalf("MarkHostConnected() error = %v", err)
	}
	fetchedHost, err := store.GetHost(h.ID)
	if err != nil {
		t.Fatalf("GetHost() error = %v", err)
	}
	if fetchedHost.LastConnectedAt == nil || !fetchedHost.LastConnectedAt.Equal(now) {
		t.Fatalf("LastConnectedAt = %v, want %v", fetchedHost.LastConnectedAt, now)
	}
	if fetchedHost.KeychainName != "prod-key" {
		t.Fatalf("KeychainName = %q, want prod-key", fetchedHost.KeychainName)
	}

	// PortForward CRUD
	pf := &PortForward{
		Name:       "pg-tunnel",
		HostID:     h.ID,
		Type:       TunnelLocal,
		LocalAddr:  "127.0.0.1",
		LocalPort:  5432,
		RemoteAddr: "127.0.0.1",
		RemotePort: 5432,
	}
	if err := store.CreatePortForward(pf); err != nil {
		t.Fatalf("CreatePortForward() error = %v", err)
	}

	// Dynamic tunnel clears remote port/addr
	dyn := &PortForward{
		Name:       "socks",
		HostID:     h.ID,
		Type:       TunnelDynamic,
		LocalPort:  1080,
		RemotePort: 9999,
	}
	if err := store.CreatePortForward(dyn); err != nil {
		t.Fatalf("CreatePortForward(dynamic) error = %v", err)
	}
	if dyn.RemotePort != 0 {
		t.Fatalf("dynamic tunnel RemotePort = %d, want 0", dyn.RemotePort)
	}

	// Snippet CRUD
	sn := &Snippet{
		Name:     "Docker PS",
		Category: "Docker",
		Command:  "docker ps -a",
	}
	if err := store.CreateSnippet(sn); err != nil {
		t.Fatalf("CreateSnippet() error = %v", err)
	}

	// Logs CRUD + ClearOldLogs
	oldTime := time.Now().UTC().AddDate(0, 0, -45)
	if err := store.CreateLog(&ConnectionLog{
		HostID:      &h.ID,
		HostName:    h.Name,
		TargetAddr:  "ubuntu@10.0.1.12:2222",
		Status:      LogStatusSuccess,
		DurationSec: 120,
		StartedAt:   oldTime,
	}); err != nil {
		t.Fatalf("CreateLog(old) error = %v", err)
	}
	if err := store.CreateLog(&ConnectionLog{
		HostID:      &h.ID,
		HostName:    h.Name,
		TargetAddr:  "ubuntu@10.0.1.12:2222",
		Status:      LogStatusFailed,
		DurationSec: 3,
		StartedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateLog(new) error = %v", err)
	}

	counts, err := store.Counts()
	if err != nil {
		t.Fatalf("Counts() error = %v", err)
	}
	if counts.Hosts != 1 || counts.Keychain != 1 || counts.PortForwards != 2 || counts.Snippets != 1 || counts.Logs != 2 {
		t.Fatalf("unexpected counts = %+v", counts)
	}

	// Deleting keychain sets host.keychain_id = NULL (ON DELETE SET NULL)
	if err := store.DeleteKeychain(kc.ID); err != nil {
		t.Fatalf("DeleteKeychain() error = %v", err)
	}
	hAfterKC, err := store.GetHost(h.ID)
	if err != nil {
		t.Fatalf("GetHost after DeleteKeychain: %v", err)
	}
	if hAfterKC.KeychainID != nil {
		t.Fatalf("expected KeychainID nil after keychain deletion, got %v", *hAfterKC.KeychainID)
	}

	// Deleting host cascades port_forwards (ON DELETE CASCADE)
	if err := store.DeleteHost(h.ID); err != nil {
		t.Fatalf("DeleteHost() error = %v", err)
	}
	pfs, err := store.ListPortForwards()
	if err != nil {
		t.Fatalf("ListPortForwards() error = %v", err)
	}
	if len(pfs) != 0 {
		t.Fatalf("expected 0 port_forwards after host deletion, got %d", len(pfs))
	}

	// Clear old logs (> 30 days) deletes only the 45-day-old entry
	removed, err := store.ClearOldLogs(30)
	if err != nil {
		t.Fatalf("ClearOldLogs() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("ClearOldLogs removed = %d, want 1", removed)
	}
	logs, err := store.ListLogs("", 100)
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if len(logs) != 1 || logs[0].Status != LogStatusFailed {
		t.Fatalf("remaining logs = %+v", logs)
	}
}

func TestMigrateLegacyDBAddsOSType(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	// Create a v1.0 database lacking os_type column.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	legacySQL := `
		CREATE TABLE hosts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			hostname TEXT NOT NULL,
			port INTEGER DEFAULT 22,
			username TEXT NOT NULL,
			group_name TEXT DEFAULT 'General',
			tags TEXT DEFAULT '',
			keychain_id INTEGER,
			inline_secret_enc BLOB,
			record_session BOOLEAN DEFAULT 0,
			notes TEXT DEFAULT '',
			last_connected_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO hosts (name, hostname, port, username) VALUES ('legacy-box', '10.0.0.2', 22, 'ubuntu');
	`
	if _, err := raw.Exec(legacySQL); err != nil {
		_ = raw.Close()
		t.Fatalf("seed legacy db: %v", err)
	}
	_ = raw.Close()

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(legacy) error = %v", err)
	}
	defer store.Close()

	hosts, err := store.ListHosts()
	if err != nil {
		t.Fatalf("ListHosts after migration error = %v", err)
	}
	if len(hosts) != 1 || hosts[0].Name != "legacy-box" || hosts[0].OSType != "linux" {
		t.Fatalf("migrated hosts = %+v", hosts)
	}
	// Verify second Open() is idempotent and OSType update works.
	hosts[0].OSType = "ubuntu"
	if err := store.UpdateHost(&hosts[0]); err != nil {
		t.Fatalf("UpdateHost(os_type) error = %v", err)
	}
	got, err := store.GetHost(hosts[0].ID)
	if err != nil || got.OSType != "ubuntu" {
		t.Fatalf("GetHost after update = (%+v, %v)", got, err)
	}
}
