package crypto

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestVaultRoundTripAndPermissions(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, ".vault.key")

	v1, err := NewVault(keyPath)
	if err != nil {
		t.Fatalf("NewVault() error = %v", err)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat vault key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("vault key perm = %o, want 0600", perm)
	}

	// Empty plaintext returns nil, nil.
	encEmpty, err := v1.Encrypt("")
	if err != nil || encEmpty != nil {
		t.Fatalf("Encrypt empty = (%v, %v), want (nil, nil)", encEmpty, err)
	}
	decEmpty, err := v1.Decrypt(nil)
	if err != nil || decEmpty != "" {
		t.Fatalf("Decrypt empty = (%q, %v), want (\"\", nil)", decEmpty, err)
	}

	secret := "s3cr3t!P@ssphrase_123"
	ct1, err := v1.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	ct2, err := v1.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt() second call error = %v", err)
	}
	if bytes.Equal(ct1, ct2) {
		t.Fatalf("Encrypt produced identical ciphertexts; nonce must be unique")
	}

	// Reload vault from disk and verify decryption.
	v2, err := NewVault(keyPath)
	if err != nil {
		t.Fatalf("NewVault reload error = %v", err)
	}
	got, err := v2.Decrypt(ct1)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if got != secret {
		t.Fatalf("Decrypt() = %q, want %q", got, secret)
	}

	// Tampered ciphertext must fail authentication.
	tampered := append([]byte(nil), ct1...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := v2.Decrypt(tampered); err == nil {
		t.Fatalf("Decrypt(tampered) succeeded, expected error")
	}

	// Short ciphertext must fail.
	if _, err := v2.Decrypt([]byte{1, 2, 3}); err == nil {
		t.Fatalf("Decrypt(short) succeeded, expected error")
	}
}

func TestVaultRejectsInvalidKeyLength(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, ".vault.key")
	if err := os.WriteFile(keyPath, []byte("too-short"), 0o600); err != nil {
		t.Fatalf("write invalid key: %v", err)
	}
	if _, err := NewVault(keyPath); err == nil {
		t.Fatalf("NewVault() accepted invalid key length")
	}
}

func TestZeroBytes(t *testing.T) {
	b := []byte("sensitive")
	ZeroBytes(b)
	for i, v := range b {
		if v != 0 {
			t.Fatalf("byte %d = %d, want 0", i, v)
		}
	}
}
