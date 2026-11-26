package ssh

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	xknownhosts "golang.org/x/crypto/ssh/knownhosts"
)

var knownHostsMu sync.Mutex

// KnownHostEntry represents a parsed entry in ~/.ssh/known_hosts.
type KnownHostEntry struct {
	LineIndex   int
	RawLine     string
	Marker      string
	Hosts       string
	IsHashed    bool
	KeyType     string
	Fingerprint string
	Comment     string
}

// ParseKnownHosts reads and parses an OpenSSH known_hosts file.
// Missing files return an empty slice and nil error.
func ParseKnownHosts(path string) ([]KnownHostEntry, error) {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read known_hosts: %w", err)
	}

	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	entries := make([]KnownHostEntry, 0, len(lines))
	for idx, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			continue
		}

		pos := 0
		marker := ""
		if strings.HasPrefix(fields[0], "@") {
			marker = fields[0]
			pos = 1
		}
		if len(fields)-pos < 3 {
			continue
		}

		hosts := fields[pos]
		keyType := fields[pos+1]
		keyB64 := fields[pos+2]
		comment := ""
		if len(fields) > pos+3 {
			comment = strings.Join(fields[pos+3:], " ")
		}

		fp := "(invalid key)"
		if rawKey, err := base64.StdEncoding.DecodeString(keyB64); err == nil {
			if pubKey, err := ssh.ParsePublicKey(rawKey); err == nil {
				fp = ssh.FingerprintSHA256(pubKey)
			}
		}

		entries = append(entries, KnownHostEntry{
			LineIndex:   idx,
			RawLine:     raw,
			Marker:      marker,
			Hosts:       hosts,
			IsHashed:    strings.HasPrefix(hosts, "|1|"),
			KeyType:     keyType,
			Fingerprint: fp,
			Comment:     comment,
		})
	}
	return entries, nil
}

// DeleteKnownHostEntry safely removes a single line from known_hosts using a
// .bak backup and atomic temp-file rename.
func DeleteKnownHostEntry(path string, entry KnownHostEntry) error {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read known_hosts: %w", err)
	}

	// Preserve backup copy before modifying.
	backupPath := path + ".bak"
	if err := writeFileAtomic(backupPath, data, 0o600); err != nil {
		return fmt.Errorf("write backup %s: %w", backupPath, err)
	}

	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	targetIdx := -1
	if entry.LineIndex >= 0 && entry.LineIndex < len(lines) && lines[entry.LineIndex] == entry.RawLine {
		targetIdx = entry.LineIndex
	} else {
		// Check for a unique exact match if lines shifted; refuse if ambiguous.
		matches := 0
		for i, l := range lines {
			if l == entry.RawLine {
				targetIdx = i
				matches++
			}
		}
		if matches != 1 {
			return errors.New("known_hosts changed on disk; press [r] to refresh before deleting")
		}
	}

	filtered := make([]string, 0, len(lines)-1)
	for i, l := range lines {
		if i == targetIdx {
			continue
		}
		filtered = append(filtered, l)
	}

	out := strings.Join(filtered, "\n")
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}

	if err := writeFileAtomic(path, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write known_hosts: %w", err)
	}
	return nil
}

// AppendKnownHost appends a host key line in standard OpenSSH format.
func AppendKnownHost(path, hostPort string, remote net.Addr, key ssh.PublicKey) error {
	if key == nil {
		return errors.New("nil public key")
	}

	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create ssh dir: %w", err)
	}

	addr := xknownhosts.Normalize(hostPort)
	if addr == "" && remote != nil {
		addr = xknownhosts.Normalize(remote.String())
	}
	line := xknownhosts.Line([]string{addr}, key)

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read known_hosts for append: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open known_hosts for append: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString(line)
	buf.WriteByte('\n')

	if _, err := f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("append known_hosts: %w", err)
	}
	return f.Sync()
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return os.Chmod(path, perm)
}
