package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func scanSnippet(row scanner) (Snippet, error) {
	var sn Snippet
	var createdRaw any
	err := row.Scan(&sn.ID, &sn.Name, &sn.Category, &sn.Command, &createdRaw)
	if err != nil {
		return Snippet{}, err
	}
	created, err := parseTime(createdRaw)
	if err != nil {
		return Snippet{}, err
	}
	sn.CreatedAt = created
	return sn, nil
}

// ListSnippets returns all saved command snippets ordered by category and name.
func (s *Store) ListSnippets() ([]Snippet, error) {
	rows, err := s.db.Query(`
		SELECT id, name, category, command, created_at
		FROM snippets
		ORDER BY LOWER(category) ASC, LOWER(name) ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list snippets: %w", err)
	}
	defer rows.Close()

	var items []Snippet
	for rows.Next() {
		sn, err := scanSnippet(rows)
		if err != nil {
			return nil, fmt.Errorf("scan snippet: %w", err)
		}
		items = append(items, sn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate snippets: %w", err)
	}
	return items, nil
}

// GetSnippet returns a snippet by ID.
func (s *Store) GetSnippet(id int64) (*Snippet, error) {
	row := s.db.QueryRow(`
		SELECT id, name, category, command, created_at
		FROM snippets
		WHERE id = ?
	`, id)
	sn, err := scanSnippet(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("snippet %d not found: %w", id, err)
		}
		return nil, fmt.Errorf("get snippet %d: %w", id, err)
	}
	return &sn, nil
}

func validateSnippet(sn *Snippet) error {
	sn.Name = strings.TrimSpace(sn.Name)
	sn.Category = strings.TrimSpace(sn.Category)
	if sn.Category == "" {
		sn.Category = "General"
	}
	sn.Command = strings.TrimSpace(sn.Command)
	if sn.Name == "" || sn.Command == "" {
		return errors.New("snippet name and command are required")
	}
	return nil
}

// CreateSnippet creates a new command snippet.
func (s *Store) CreateSnippet(sn *Snippet) error {
	if err := validateSnippet(sn); err != nil {
		return err
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(`
		INSERT INTO snippets (name, category, command, created_at)
		VALUES (?, ?, ?, ?)
	`, sn.Name, sn.Category, sn.Command, now.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("create snippet: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("snippet last insert id: %w", err)
	}
	sn.ID = id
	sn.CreatedAt = now
	return nil
}

// UpdateSnippet updates an existing snippet.
func (s *Store) UpdateSnippet(sn *Snippet) error {
	if sn.ID <= 0 {
		return errors.New("invalid snippet id")
	}
	if err := validateSnippet(sn); err != nil {
		return err
	}
	res, err := s.db.Exec(`
		UPDATE snippets SET name = ?, category = ?, command = ?
		WHERE id = ?
	`, sn.Name, sn.Category, sn.Command, sn.ID)
	if err != nil {
		return fmt.Errorf("update snippet: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("snippet %d not found", sn.ID)
	}
	return nil
}

// DeleteSnippet removes a snippet by ID.
func (s *Store) DeleteSnippet(id int64) error {
	res, err := s.db.Exec(`DELETE FROM snippets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete snippet: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("snippet %d not found", id)
	}
	return nil
}
