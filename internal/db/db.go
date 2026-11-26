package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS keychain (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL CHECK(type IN ('password', 'ssh_key')),
    username TEXT DEFAULT '',
    secret_enc BLOB,
    private_key_path TEXT DEFAULT '',
    public_key TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS hosts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    hostname TEXT NOT NULL,
    port INTEGER DEFAULT 22,
    username TEXT NOT NULL,
    os_type TEXT DEFAULT 'linux',
    group_name TEXT DEFAULT 'General',
    tags TEXT DEFAULT '',
    keychain_id INTEGER,
    inline_secret_enc BLOB,
    record_session BOOLEAN DEFAULT 0,
    notes TEXT DEFAULT '',
    last_connected_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (keychain_id) REFERENCES keychain(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS port_forwards (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    host_id INTEGER NOT NULL,
    type TEXT NOT NULL CHECK(type IN ('local', 'remote', 'dynamic')),
    local_addr TEXT DEFAULT '127.0.0.1',
    local_port INTEGER NOT NULL,
    remote_addr TEXT DEFAULT '127.0.0.1',
    remote_port INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (host_id) REFERENCES hosts(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS snippets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    category TEXT DEFAULT 'General',
    command TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS connection_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    host_id INTEGER,
    host_name TEXT NOT NULL,
    target_addr TEXT NOT NULL,
    status TEXT NOT NULL,
    duration_sec INTEGER DEFAULT 0,
    transcript_path TEXT DEFAULT '',
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_hosts_name ON hosts(name);
CREATE INDEX IF NOT EXISTS idx_logs_started_at ON connection_logs(started_at DESC);
`

// Store wraps the SQLite database connection and provides repository methods.
type Store struct {
	db *sql.DB
}

// Open initializes the SQLite database at dbPath, enforces 0600 permissions,
// enables foreign keys and WAL mode, and runs schema migrations.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("create db parent dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}

	// Single open connection prevents SQLITE_BUSY in single-process TUI with goroutines.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA synchronous = NORMAL;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("execute %s: %w", p, err)
		}
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	if err := migrateSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("incremental schema migration: %w", err)
	}

	if err := os.Chmod(dbPath, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("chmod db %s: %w", dbPath, err)
	}

	return &Store{db: db}, nil
}

func migrateSchema(db *sql.DB) error {
	hasOSType, err := hasColumn(db, "hosts", "os_type")
	if err != nil {
		return err
	}
	if !hasOSType {
		if _, err := db.Exec(`ALTER TABLE hosts ADD COLUMN os_type TEXT DEFAULT 'linux';`); err != nil {
			return fmt.Errorf("add hosts.os_type column: %w", err)
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, col string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s);`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultVal, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, col) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Close closes the underlying SQLite connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Counts returns current item counts for all DB-backed modules.
func (s *Store) Counts() (Counts, error) {
	var c Counts
	row := s.db.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM hosts),
			(SELECT COUNT(*) FROM keychain),
			(SELECT COUNT(*) FROM port_forwards),
			(SELECT COUNT(*) FROM snippets),
			(SELECT COUNT(*) FROM connection_logs)
	`)
	if err := row.Scan(&c.Hosts, &c.Keychain, &c.PortForwards, &c.Snippets, &c.Logs); err != nil {
		return Counts{}, fmt.Errorf("query counts: %w", err)
	}
	return c, nil
}

var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

func parseNullTime(v any) (*time.Time, error) {
	if v == nil {
		return nil, nil
	}
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return nil, nil
		}
		utc := t.UTC()
		return &utc, nil
	case []byte:
		return parseTimeString(string(t))
	case string:
		return parseTimeString(t)
	default:
		return nil, fmt.Errorf("unsupported datetime type %T", v)
	}
}

func parseTimeString(raw string) (*time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	for _, layout := range timeLayouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			utc := parsed.UTC()
			return &utc, nil
		}
	}
	return nil, fmt.Errorf("parse datetime %q: unrecognized format", raw)
}

func parseTime(v any) (time.Time, error) {
	ptr, err := parseNullTime(v)
	if err != nil {
		return time.Time{}, err
	}
	if ptr == nil {
		return time.Time{}, nil
	}
	return *ptr, nil
}
