package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds resolved paths for ez-ssh local storage and OpenSSH files.
type Config struct {
	ConfigDir      string
	LogsDir        string
	RunDir         string
	DBPath         string
	VaultKeyPath   string
	SSHDir         string
	KnownHostsPath string
	SSHConfigPath  string
}

// Load resolves all configuration paths, ensures local directories exist with
// 0700 permissions, and returns the initialized Config.
func Load() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}

	configDir, err := resolveConfigDir(home)
	if err != nil {
		return nil, err
	}

	sshDir := filepath.Join(home, ".ssh")
	if v := envOrLegacy("EZ_SSH_DIR", "LAZY_SSH_DIR"); v != "" {
		sshDir = ExpandPath(v)
	}

	cfg := &Config{
		ConfigDir:      configDir,
		LogsDir:        filepath.Join(configDir, "logs"),
		RunDir:         filepath.Join(configDir, "run"),
		DBPath:         filepath.Join(configDir, "vault.db"),
		VaultKeyPath:   filepath.Join(configDir, ".vault.key"),
		SSHDir:         sshDir,
		KnownHostsPath: filepath.Join(sshDir, "known_hosts"),
		SSHConfigPath:  filepath.Join(sshDir, "config"),
	}

	for _, dir := range []string{cfg.ConfigDir, cfg.LogsDir, cfg.RunDir} {
		if err := ensureSecureDir(dir); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

// envOrLegacy prefers the new EZ_SSH_* variable while still honouring the old
// LAZY_SSH_* name, so existing installations keep working after the rename.
func envOrLegacy(name, legacy string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(legacy))
}

func resolveConfigDir(home string) (string, error) {
	if override := envOrLegacy("EZ_SSH_CONFIG_DIR", "LAZY_SSH_CONFIG_DIR"); override != "" {
		abs, err := filepath.Abs(ExpandPath(override))
		if err != nil {
			return "", fmt.Errorf("resolve EZ_SSH_CONFIG_DIR: %w", err)
		}
		return abs, nil
	}

	var dir string
	var legacyDir string
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		dir = filepath.Join(ExpandPath(xdg), "ez-ssh")
		legacyDir = filepath.Join(ExpandPath(xdg), "lazy-ssh")
	} else {
		dir = filepath.Join(home, ".config", "ez-ssh")
		legacyDir = filepath.Join(home, ".config", "lazy-ssh")
	}
	migrateLegacyConfigDir(dir, legacyDir)
	return dir, nil
}

// migrateLegacyConfigDir carries the vault and host database over to the new
// directory on first launch after the rename. It never overwrites an existing
// ez-ssh directory.
func migrateLegacyConfigDir(newDir, oldDir string) {
	if newDir == oldDir {
		return
	}
	if _, err := os.Stat(newDir); err == nil {
		return
	}
	if _, err := os.Stat(oldDir); err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(newDir), 0o700); err == nil {
		_ = os.Rename(oldDir, newDir)
	}
}

func ensureSecureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod directory %s: %w", dir, err)
	}
	return nil
}

// ExpandPath expands a leading "~" or "~/" to the user's home directory.
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, "~\\") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
