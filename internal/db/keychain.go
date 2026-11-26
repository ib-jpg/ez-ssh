package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func scanKeychain(row scanner) (KeychainItem, error) {
	var k KeychainItem
	var username sql.NullString
	var privPath sql.NullString
	var pubKey sql.NullString
	var createdRaw any

	err := row.Scan(
		&k.ID,
		&k.Name,
		&k.Type,
		&username,
		&k.SecretEnc,
		&privPath,
		&pubKey,
		&createdRaw,
	)
	if err != nil {
		return KeychainItem{}, err
	}
	k.Username = username.String
	k.PrivateKeyPath = privPath.String
	k.PublicKey = pubKey.String

	created, err := parseTime(createdRaw)
	if err != nil {
		return KeychainItem{}, err
	}
	k.CreatedAt = created
	return k, nil
}

// ListKeychain returns all keychain entries sorted by type and name.
func (s *Store) ListKeychain() ([]KeychainItem, error) {
	rows, err := s.db.Query(`
		SELECT id, name, type, username, secret_enc, private_key_path, public_key, created_at
		FROM keychain
		ORDER BY type ASC, LOWER(name) ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list keychain: %w", err)
	}
	defer rows.Close()

	var items []KeychainItem
	for rows.Next() {
		item, err := scanKeychain(rows)
		if err != nil {
			return nil, fmt.Errorf("scan keychain: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate keychain: %w", err)
	}
	return items, nil
}

// GetKeychain fetches a keychain entry by ID.
func (s *Store) GetKeychain(id int64) (*KeychainItem, error) {
	row := s.db.QueryRow(`
		SELECT id, name, type, username, secret_enc, private_key_path, public_key, created_at
		FROM keychain
		WHERE id = ?
	`, id)
	item, err := scanKeychain(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("keychain item %d not found: %w", id, err)
		}
		return nil, fmt.Errorf("get keychain %d: %w", id, err)
	}
	return &item, nil
}

// GetKeychainByPath finds an ssh_key keychain entry matching private_key_path.
func (s *Store) GetKeychainByPath(privPath string) (*KeychainItem, error) {
	row := s.db.QueryRow(`
		SELECT id, name, type, username, secret_enc, private_key_path, public_key, created_at
		FROM keychain
		WHERE type = 'ssh_key' AND private_key_path = ?
		LIMIT 1
	`, strings.TrimSpace(privPath))
	item, err := scanKeychain(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get keychain by path: %w", err)
	}
	return &item, nil
}

// GetKeychainByName finds a keychain entry by its unique name.
func (s *Store) GetKeychainByName(name string) (*KeychainItem, error) {
	row := s.db.QueryRow(`
		SELECT id, name, type, username, secret_enc, private_key_path, public_key, created_at
		FROM keychain
		WHERE name = ?
	`, strings.TrimSpace(name))
	item, err := scanKeychain(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get keychain by name: %w", err)
	}
	return &item, nil
}

func validateKeychainItem(k *KeychainItem) error {
	k.Name = strings.TrimSpace(k.Name)
	k.Username = strings.TrimSpace(k.Username)
	k.PrivateKeyPath = strings.TrimSpace(k.PrivateKeyPath)
	k.PublicKey = strings.TrimSpace(k.PublicKey)

	if k.Name == "" {
		return errors.New("keychain name is required")
	}
	if k.Type != KeychainPassword && k.Type != KeychainSSHKey {
		return fmt.Errorf("invalid keychain type %q", k.Type)
	}
	if k.Type == KeychainSSHKey && k.PrivateKeyPath == "" {
		return errors.New("private key path is required for ssh_key")
	}
	return nil
}

// CreateKeychain inserts a new keychain item.
func (s *Store) CreateKeychain(k *KeychainItem) error {
	if err := validateKeychainItem(k); err != nil {
		return err
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(`
		INSERT INTO keychain (name, type, username, secret_enc, private_key_path, public_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, k.Name, k.Type, k.Username, k.SecretEnc, k.PrivateKeyPath, k.PublicKey, now.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("create keychain: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("keychain last insert id: %w", err)
	}
	k.ID = id
	k.CreatedAt = now
	return nil
}

// UpdateKeychain updates an existing keychain item.
func (s *Store) UpdateKeychain(k *KeychainItem) error {
	if k.ID <= 0 {
		return errors.New("invalid keychain id")
	}
	if err := validateKeychainItem(k); err != nil {
		return err
	}
	res, err := s.db.Exec(`
		UPDATE keychain SET
			name = ?, type = ?, username = ?, secret_enc = ?,
			private_key_path = ?, public_key = ?
		WHERE id = ?
	`, k.Name, k.Type, k.Username, k.SecretEnc, k.PrivateKeyPath, k.PublicKey, k.ID)
	if err != nil {
		return fmt.Errorf("update keychain: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("keychain item %d not found", k.ID)
	}
	return nil
}

// DeleteKeychain removes a keychain item (hosts referencing it get keychain_id=NULL).
func (s *Store) DeleteKeychain(id int64) error {
	res, err := s.db.Exec(`DELETE FROM keychain WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete keychain: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("keychain item %d not found", id)
	}
	return nil
}
