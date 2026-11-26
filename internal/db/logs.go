package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func scanLog(row scanner) (ConnectionLog, error) {
	var l ConnectionLog
	var hostID sql.NullInt64
	var startedRaw any

	err := row.Scan(
		&l.ID,
		&hostID,
		&l.HostName,
		&l.TargetAddr,
		&l.Status,
		&l.DurationSec,
		&l.TranscriptPath,
		&startedRaw,
	)
	if err != nil {
		return ConnectionLog{}, err
	}
	if hostID.Valid {
		id := hostID.Int64
		l.HostID = &id
	}
	started, err := parseTime(startedRaw)
	if err != nil {
		return ConnectionLog{}, err
	}
	l.StartedAt = started
	return l, nil
}

// ListLogs returns recent connection logs, optionally filtered by exact host_name.
func (s *Store) ListLogs(hostFilter string, limit int) ([]ConnectionLog, error) {
	if limit <= 0 {
		limit = 500
	}
	hostFilter = strings.TrimSpace(hostFilter)

	var (
		rows *sql.Rows
		err  error
	)
	if hostFilter != "" {
		rows, err = s.db.Query(`
			SELECT id, host_id, host_name, target_addr, status, duration_sec, transcript_path, started_at
			FROM connection_logs
			WHERE host_name = ?
			ORDER BY started_at DESC, id DESC
			LIMIT ?
		`, hostFilter, limit)
	} else {
		rows, err = s.db.Query(`
			SELECT id, host_id, host_name, target_addr, status, duration_sec, transcript_path, started_at
			FROM connection_logs
			ORDER BY started_at DESC, id DESC
			LIMIT ?
		`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list connection_logs: %w", err)
	}
	defer rows.Close()

	var logs []ConnectionLog
	for rows.Next() {
		l, err := scanLog(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connection_log: %w", err)
		}
		logs = append(logs, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connection_logs: %w", err)
	}
	return logs, nil
}

// ListDistinctLogHosts returns distinct host names present in connection_logs.
func (s *Store) ListDistinctLogHosts() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT host_name FROM connection_logs
		WHERE host_name <> ''
		ORDER BY LOWER(host_name) ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list distinct log hosts: %w", err)
	}
	defer rows.Close()

	var hosts []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		hosts = append(hosts, name)
	}
	return hosts, rows.Err()
}

// CreateLog records a connection or snippet execution audit entry.
func (s *Store) CreateLog(l *ConnectionLog) error {
	l.HostName = strings.TrimSpace(l.HostName)
	l.TargetAddr = strings.TrimSpace(l.TargetAddr)
	l.Status = strings.TrimSpace(l.Status)
	if l.HostName == "" || l.TargetAddr == "" || l.Status == "" {
		return errors.New("log host_name, target_addr, and status are required")
	}

	started := l.StartedAt.UTC()
	if started.IsZero() {
		started = time.Now().UTC()
	}

	var hostID any
	if l.HostID != nil {
		hostID = *l.HostID
	}

	res, err := s.db.Exec(`
		INSERT INTO connection_logs (
			host_id, host_name, target_addr, status, duration_sec, transcript_path, started_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, hostID, l.HostName, l.TargetAddr, l.Status, l.DurationSec, l.TranscriptPath, started.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("create connection_log: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("connection_log last insert id: %w", err)
	}
	l.ID = id
	l.StartedAt = started
	return nil
}

// ClearOldLogs deletes log rows older than olderThanDays days and returns rows removed.
func (s *Store) ClearOldLogs(olderThanDays int) (int64, error) {
	if olderThanDays < 0 {
		olderThanDays = 0
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -olderThanDays).Format(time.RFC3339)
	res, err := s.db.Exec(`DELETE FROM connection_logs WHERE started_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("clear old logs: %w", err)
	}
	return res.RowsAffected()
}

// ClearAllLogs removes all entries from connection_logs.
func (s *Store) ClearAllLogs() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM connection_logs`)
	if err != nil {
		return 0, fmt.Errorf("clear all logs: %w", err)
	}
	return res.RowsAffected()
}
