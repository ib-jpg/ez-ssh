package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type scanner interface {
	Scan(dest ...any) error
}

func scanHost(row scanner) (Host, error) {
	var h Host
	var keychainID sql.NullInt64
	var keychainName sql.NullString
	var keychainType sql.NullString
	var lastConnRaw any
	var createdRaw any

	err := row.Scan(
		&h.ID,
		&h.Name,
		&h.Hostname,
		&h.Port,
		&h.Username,
		&h.OSType,
		&h.GroupName,
		&h.Tags,
		&keychainID,
		&keychainName,
		&keychainType,
		&h.InlineSecretEnc,
		&h.RecordSession,
		&h.Notes,
		&lastConnRaw,
		&createdRaw,
	)
	if err != nil {
		return Host{}, err
	}

	if keychainID.Valid {
		id := keychainID.Int64
		h.KeychainID = &id
	}
	h.KeychainName = keychainName.String
	h.KeychainType = keychainType.String

	lastConn, err := parseNullTime(lastConnRaw)
	if err != nil {
		return Host{}, err
	}
	h.LastConnectedAt = lastConn

	created, err := parseTime(createdRaw)
	if err != nil {
		return Host{}, err
	}
	h.CreatedAt = created

	return h, nil
}

const hostSelectColumns = `
	h.id, h.name, h.hostname, h.port, h.username, COALESCE(h.os_type, 'linux'),
	h.group_name, h.tags,
	h.keychain_id, k.name, k.type, h.inline_secret_enc, h.record_session,
	h.notes, h.last_connected_at, h.created_at
`

// ListHosts returns all hosts ordered by group and name.
func (s *Store) ListHosts() ([]Host, error) {
	rows, err := s.db.Query(`
		SELECT ` + hostSelectColumns + `
		FROM hosts h
		LEFT JOIN keychain k ON h.keychain_id = k.id
		ORDER BY LOWER(h.group_name) ASC, LOWER(h.name) ASC, h.id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	defer rows.Close()

	var hosts []Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, fmt.Errorf("scan host: %w", err)
		}
		hosts = append(hosts, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hosts: %w", err)
	}
	return hosts, nil
}

// GetHost fetches a single host by ID.
func (s *Store) GetHost(id int64) (*Host, error) {
	row := s.db.QueryRow(`
		SELECT `+hostSelectColumns+`
		FROM hosts h
		LEFT JOIN keychain k ON h.keychain_id = k.id
		WHERE h.id = ?
	`, id)
	h, err := scanHost(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("host %d not found: %w", id, err)
		}
		return nil, fmt.Errorf("get host %d: %w", id, err)
	}
	return &h, nil
}

func normalizeHost(h *Host) {
	h.Name = strings.TrimSpace(h.Name)
	h.Hostname = strings.TrimSpace(h.Hostname)
	h.Username = strings.TrimSpace(h.Username)
	if h.Port <= 0 {
		h.Port = 22
	}
	h.OSType = strings.ToLower(strings.TrimSpace(h.OSType))
	if h.OSType == "" {
		h.OSType = "linux"
	}
	h.GroupName = strings.TrimSpace(h.GroupName)
	if h.GroupName == "" {
		h.GroupName = "General"
	}
	h.Tags = strings.TrimSpace(h.Tags)
}

// CreateHost inserts a new host record and populates h.ID and h.CreatedAt.
func (s *Store) CreateHost(h *Host) error {
	normalizeHost(h)
	if h.Name == "" || h.Hostname == "" || h.Username == "" {
		return errors.New("host name, hostname, and username are required")
	}

	now := time.Now().UTC()
	var kcID any
	if h.KeychainID != nil {
		kcID = *h.KeychainID
	}

	res, err := s.db.Exec(`
		INSERT INTO hosts (
			name, hostname, port, username, os_type, group_name, tags,
			keychain_id, inline_secret_enc, record_session, notes, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		h.Name, h.Hostname, h.Port, h.Username, h.OSType, h.GroupName, h.Tags,
		kcID, h.InlineSecretEnc, h.RecordSession, h.Notes, now.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("create host: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("host last insert id: %w", err)
	}
	h.ID = id
	h.CreatedAt = now
	return nil
}

// UpdateHost updates an existing host record.
func (s *Store) UpdateHost(h *Host) error {
	normalizeHost(h)
	if h.ID <= 0 {
		return errors.New("invalid host id")
	}
	if h.Name == "" || h.Hostname == "" || h.Username == "" {
		return errors.New("host name, hostname, and username are required")
	}

	var kcID any
	if h.KeychainID != nil {
		kcID = *h.KeychainID
	}

	res, err := s.db.Exec(`
		UPDATE hosts SET
			name = ?, hostname = ?, port = ?, username = ?, os_type = ?, group_name = ?,
			tags = ?, keychain_id = ?, inline_secret_enc = ?,
			record_session = ?, notes = ?
		WHERE id = ?
	`,
		h.Name, h.Hostname, h.Port, h.Username, h.OSType, h.GroupName,
		h.Tags, kcID, h.InlineSecretEnc, h.RecordSession, h.Notes, h.ID,
	)
	if err != nil {
		return fmt.Errorf("update host: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("host %d not found", h.ID)
	}
	return nil
}

// DeleteHost removes a host by ID (cascades port_forwards).
func (s *Store) DeleteHost(id int64) error {
	res, err := s.db.Exec(`DELETE FROM hosts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete host: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("host %d not found", id)
	}
	return nil
}

// MarkHostConnected updates last_connected_at for the given host.
func (s *Store) MarkHostConnected(id int64, at time.Time) error {
	_, err := s.db.Exec(
		`UPDATE hosts SET last_connected_at = ? WHERE id = ?`,
		at.UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("mark host connected: %w", err)
	}
	return nil
}

// HostExists checks if a host with the same name OR same (hostname, port, username)
// already exists in the database.
func (s *Store) HostExists(name, hostname string, port int, username string) (bool, error) {
	if port <= 0 {
		port = 22
	}
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM hosts
		WHERE LOWER(name) = LOWER(?)
		   OR (LOWER(hostname) = LOWER(?) AND port = ? AND username = ?)
	`, strings.TrimSpace(name), strings.TrimSpace(hostname), port, strings.TrimSpace(username)).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check host exists: %w", err)
	}
	return count > 0, nil
}
