package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func scanPortForward(row scanner) (PortForward, error) {
	var pf PortForward
	var hostName string
	var hostname string
	var port int
	var username string
	var createdRaw any

	err := row.Scan(
		&pf.ID,
		&pf.Name,
		&pf.HostID,
		&hostName,
		&username,
		&hostname,
		&port,
		&pf.Type,
		&pf.LocalAddr,
		&pf.LocalPort,
		&pf.RemoteAddr,
		&pf.RemotePort,
		&createdRaw,
	)
	if err != nil {
		return PortForward{}, err
	}

	pf.HostName = hostName
	pf.HostTarget = fmt.Sprintf("%s@%s:%d", username, hostname, port)

	created, err := parseTime(createdRaw)
	if err != nil {
		return PortForward{}, err
	}
	pf.CreatedAt = created
	return pf, nil
}

const portForwardSelectColumns = `
	p.id, p.name, p.host_id, h.name, h.username, h.hostname, h.port,
	p.type, p.local_addr, p.local_port, p.remote_addr, p.remote_port, p.created_at
`

// ListPortForwards returns all port forwarding rules joined with host info.
func (s *Store) ListPortForwards() ([]PortForward, error) {
	rows, err := s.db.Query(`
		SELECT ` + portForwardSelectColumns + `
		FROM port_forwards p
		JOIN hosts h ON p.host_id = h.id
		ORDER BY LOWER(p.name) ASC, p.id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list port_forwards: %w", err)
	}
	defer rows.Close()

	var items []PortForward
	for rows.Next() {
		pf, err := scanPortForward(rows)
		if err != nil {
			return nil, fmt.Errorf("scan port_forward: %w", err)
		}
		items = append(items, pf)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate port_forwards: %w", err)
	}
	return items, nil
}

// GetPortForward fetches a single port forwarding rule by ID.
func (s *Store) GetPortForward(id int64) (*PortForward, error) {
	row := s.db.QueryRow(`
		SELECT `+portForwardSelectColumns+`
		FROM port_forwards p
		JOIN hosts h ON p.host_id = h.id
		WHERE p.id = ?
	`, id)
	pf, err := scanPortForward(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("port_forward %d not found: %w", id, err)
		}
		return nil, fmt.Errorf("get port_forward %d: %w", id, err)
	}
	return &pf, nil
}

func validatePortForward(pf *PortForward) error {
	pf.Name = strings.TrimSpace(pf.Name)
	pf.LocalAddr = strings.TrimSpace(pf.LocalAddr)
	if pf.LocalAddr == "" {
		pf.LocalAddr = "127.0.0.1"
	}
	pf.RemoteAddr = strings.TrimSpace(pf.RemoteAddr)
	if pf.RemoteAddr == "" {
		pf.RemoteAddr = "127.0.0.1"
	}

	if pf.Name == "" {
		return errors.New("tunnel name is required")
	}
	if pf.HostID <= 0 {
		return errors.New("target host is required")
	}
	if pf.LocalPort <= 0 || pf.LocalPort > 65535 {
		return errors.New("local port must be between 1 and 65535")
	}
	switch pf.Type {
	case TunnelLocal, TunnelRemote:
		if pf.RemotePort <= 0 || pf.RemotePort > 65535 {
			return errors.New("remote port must be between 1 and 65535")
		}
	case TunnelDynamic:
		pf.RemotePort = 0
		pf.RemoteAddr = ""
	default:
		return fmt.Errorf("invalid tunnel type %q", pf.Type)
	}
	return nil
}

// CreatePortForward creates a new port forwarding entry.
func (s *Store) CreatePortForward(pf *PortForward) error {
	if err := validatePortForward(pf); err != nil {
		return err
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(`
		INSERT INTO port_forwards (
			name, host_id, type, local_addr, local_port, remote_addr, remote_port, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, pf.Name, pf.HostID, pf.Type, pf.LocalAddr, pf.LocalPort, pf.RemoteAddr, pf.RemotePort, now.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("create port_forward: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("port_forward last insert id: %w", err)
	}
	pf.ID = id
	pf.CreatedAt = now
	return nil
}

// UpdatePortForward updates an existing port forwarding rule.
func (s *Store) UpdatePortForward(pf *PortForward) error {
	if pf.ID <= 0 {
		return errors.New("invalid port_forward id")
	}
	if err := validatePortForward(pf); err != nil {
		return err
	}
	res, err := s.db.Exec(`
		UPDATE port_forwards SET
			name = ?, host_id = ?, type = ?, local_addr = ?, local_port = ?,
			remote_addr = ?, remote_port = ?
		WHERE id = ?
	`, pf.Name, pf.HostID, pf.Type, pf.LocalAddr, pf.LocalPort, pf.RemoteAddr, pf.RemotePort, pf.ID)
	if err != nil {
		return fmt.Errorf("update port_forward: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("port_forward %d not found", pf.ID)
	}
	return nil
}

// DeletePortForward deletes a port forwarding rule by ID.
func (s *Store) DeletePortForward(id int64) error {
	res, err := s.db.Exec(`DELETE FROM port_forwards WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete port_forward: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("port_forward %d not found", id)
	}
	return nil
}
