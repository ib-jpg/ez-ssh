package ssh

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/ib-jpg/ez-ssh/internal/config"
)

// ImportedHost holds parsed host attributes from ~/.ssh/config.
type ImportedHost struct {
	Name         string
	Hostname     string
	Port         int
	Username     string
	IdentityFile string
}

type sshConfigBlock struct {
	patterns     []string
	hostname     string
	user         string
	port         int
	identityFile string
	isMatch      bool
}

// ParseSSHConfig parses an OpenSSH client configuration file into concrete hosts,
// inheriting global `Host *` defaults and skipping wildcard/negated patterns.
func ParseSSHConfig(path string) ([]ImportedHost, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ssh config: %w", err)
	}

	defaultUser := currentUsername()
	var global sshConfigBlock
	var blocks []sshConfigBlock
	var current *sshConfigBlock

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for _, rawLine := range lines {
		line := stripSSHComment(rawLine)
		if line == "" {
			continue
		}

		key, val := splitSSHDirective(line)
		if key == "" || val == "" {
			continue
		}

		switch strings.ToLower(key) {
		case "host":
			if current != nil && !current.isMatch {
				blocks = append(blocks, *current)
			}
			current = &sshConfigBlock{
				patterns: splitQuotedFields(val),
			}
		case "match":
			if current != nil && !current.isMatch {
				blocks = append(blocks, *current)
			}
			current = &sshConfigBlock{isMatch: true}
		case "hostname":
			target := firstToken(val)
			if current == nil {
				global.hostname = target
			} else if !current.isMatch && current.hostname == "" {
				current.hostname = target
			}
		case "user":
			u := firstToken(val)
			if current == nil {
				global.user = u
			} else if !current.isMatch && current.user == "" {
				current.user = u
			}
		case "port":
			p, err := strconv.Atoi(firstToken(val))
			if err == nil && p > 0 && p <= 65535 {
				if current == nil {
					global.port = p
				} else if !current.isMatch && current.port == 0 {
					current.port = p
				}
			}
		case "identityfile":
			idPath := expandIdentityTokens(firstToken(val), defaultUser)
			if current == nil {
				if global.identityFile == "" {
					global.identityFile = idPath
				}
			} else if !current.isMatch && current.identityFile == "" {
				current.identityFile = idPath
			}
		}
	}
	if current != nil && !current.isMatch {
		blocks = append(blocks, *current)
	}

	// Merge global `Host *` blocks into defaults (respecting first-match precedence).
	for _, b := range blocks {
		for _, pat := range b.patterns {
			if pat == "*" {
				if global.user == "" && b.user != "" {
					global.user = b.user
				}
				if global.port == 0 && b.port != 0 {
					global.port = b.port
				}
				if global.identityFile == "" && b.identityFile != "" {
					global.identityFile = b.identityFile
				}
			}
		}
	}

	var result []ImportedHost
	for _, b := range blocks {
		for _, pat := range b.patterns {
			if strings.ContainsAny(pat, "*?!") {
				continue
			}
			hostname := b.hostname
			if hostname == "" {
				hostname = pat
			}
			u := b.user
			if u == "" {
				u = global.user
			}
			if u == "" {
				u = defaultUser
			}
			port := b.port
			if port == 0 {
				port = global.port
			}
			if port == 0 {
				port = 22
			}
			idFile := b.identityFile
			if idFile == "" {
				idFile = global.identityFile
			}
			if idFile != "" {
				idFile = strings.ReplaceAll(idFile, "%h", hostname)
				idFile = strings.ReplaceAll(idFile, "%p", strconv.Itoa(port))
				idFile = strings.ReplaceAll(idFile, "%r", u)
				idFile = strings.ReplaceAll(idFile, "%n", pat)
			}

			result = append(result, ImportedHost{
				Name:         pat,
				Hostname:     hostname,
				Port:         port,
				Username:     u,
				IdentityFile: idFile,
			})
		}
	}

	return result, nil
}

func currentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if env := os.Getenv("USER"); env != "" {
		return env
	}
	return "root"
}

func expandIdentityTokens(val, username string) string {
	home, _ := os.UserHomeDir()
	if home != "" {
		val = strings.ReplaceAll(val, "%d", home)
	}
	val = strings.ReplaceAll(val, "%u", username)
	return config.ExpandPath(val)
}

func stripSSHComment(line string) string {
	inQuotes := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuotes = !inQuotes
		case '#':
			if !inQuotes {
				return strings.TrimSpace(line[:i])
			}
		}
	}
	return strings.TrimSpace(line)
}

func splitSSHDirective(line string) (string, string) {
	// Support both `Key Value` and `Key=Value` / `Key = Value`.
	for i := 0; i < len(line); i++ {
		if line[i] == ' ' || line[i] == '\t' || line[i] == '=' {
			key := strings.TrimSpace(line[:i])
			rest := strings.TrimSpace(line[i+1:])
			rest = strings.TrimPrefix(rest, "=")
			return key, strings.TrimSpace(rest)
		}
	}
	return line, ""
}

func firstToken(val string) string {
	fields := splitQuotedFields(val)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func splitQuotedFields(s string) []string {
	var fields []string
	var cur strings.Builder
	inQuotes := false
	hasToken := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuotes = !inQuotes
			hasToken = true
		case (c == ' ' || c == '\t') && !inQuotes:
			if hasToken {
				fields = append(fields, cur.String())
				cur.Reset()
				hasToken = false
			}
		default:
			cur.WriteByte(c)
			hasToken = true
		}
	}
	if hasToken {
		fields = append(fields, cur.String())
	}
	return fields
}
