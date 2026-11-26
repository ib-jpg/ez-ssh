package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUsesOverrideAndPermissions(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "custom-cfg")
	t.Setenv("EZ_SSH_CONFIG_DIR", target)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ConfigDir != target {
		t.Fatalf("ConfigDir = %q, want %q", cfg.ConfigDir, target)
	}
	if cfg.DBPath != filepath.Join(target, "vault.db") {
		t.Fatalf("DBPath = %q", cfg.DBPath)
	}
	if cfg.VaultKeyPath != filepath.Join(target, ".vault.key") {
		t.Fatalf("VaultKeyPath = %q", cfg.VaultKeyPath)
	}

	for _, dir := range []string{cfg.ConfigDir, cfg.LogsDir, cfg.RunDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("%s perm = %o, want 0700", dir, perm)
		}
	}
}

func TestLoadUsesXDGConfigHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("EZ_SSH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := filepath.Join(tmp, "ez-ssh")
	if cfg.ConfigDir != want {
		t.Fatalf("ConfigDir = %q, want %q", cfg.ConfigDir, want)
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	if got := ExpandPath("~/id_ed25519"); got != filepath.Join(home, "id_ed25519") {
		t.Fatalf("ExpandPath(~/id_ed25519) = %q", got)
	}
	if got := ExpandPath("~"); got != home {
		t.Fatalf("ExpandPath(~) = %q", got)
	}
	if got := ExpandPath("/etc/ssh/ssh_config"); got != "/etc/ssh/ssh_config" {
		t.Fatalf("ExpandPath(abs) = %q", got)
	}
	if got := ExpandPath(""); got != "" {
		t.Fatalf("ExpandPath(empty) = %q", got)
	}
}
