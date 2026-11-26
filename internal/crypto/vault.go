package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const vaultKeySize = 32 // AES-256

// Vault handles AES-256-GCM encryption and decryption backed by a local key file.
type Vault struct {
	aead cipher.AEAD
}

// NewVault loads an existing 32-byte key from keyPath or atomically generates a
// new key file with strict 0600 permissions.
func NewVault(keyPath string) (*Vault, error) {
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, fmt.Errorf("create vault key parent dir: %w", err)
	}

	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	defer ZeroBytes(key)

	if len(key) != vaultKeySize {
		return nil, fmt.Errorf("invalid vault key length %d (expected %d bytes)", len(key), vaultKeySize)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init GCM: %w", err)
	}

	return &Vault{aead: aead}, nil
}

func loadOrCreateKey(keyPath string) ([]byte, error) {
	data, err := os.ReadFile(keyPath)
	if err == nil {
		if err := os.Chmod(keyPath, 0o600); err != nil {
			ZeroBytes(data)
			return nil, fmt.Errorf("enforce 0600 on %s: %w", keyPath, err)
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read vault key: %w", err)
	}

	key := make([]byte, vaultKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate random vault key: %w", err)
	}

	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		ZeroBytes(key)
		if errors.Is(err, os.ErrExist) {
			// Another process created it concurrently; read theirs.
			return os.ReadFile(keyPath)
		}
		return nil, fmt.Errorf("create vault key file: %w", err)
	}

	writeErr := writeAllAndSync(f, key)
	closeErr := f.Close()
	if writeErr != nil {
		ZeroBytes(key)
		_ = os.Remove(keyPath)
		return nil, fmt.Errorf("write vault key: %w", writeErr)
	}
	if closeErr != nil {
		ZeroBytes(key)
		_ = os.Remove(keyPath)
		return nil, fmt.Errorf("close vault key: %w", closeErr)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		ZeroBytes(key)
		return nil, fmt.Errorf("chmod vault key: %w", err)
	}

	return key, nil
}

func writeAllAndSync(f *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := f.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return f.Sync()
}

// Encrypt encrypts plaintext with AES-256-GCM using a fresh 12-byte random nonce.
// Empty plaintext returns nil, nil so optional secrets stay NULL/empty.
func (v *Vault) Encrypt(plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	if v == nil || v.aead == nil {
		return nil, errors.New("vault is not initialized")
	}

	nonceSize := v.aead.NonceSize()
	out := make([]byte, nonceSize+len(plaintext)+v.aead.Overhead())
	nonce := out[:nonceSize]
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		ZeroBytes(out)
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	plainBytes := []byte(plaintext)
	defer ZeroBytes(plainBytes)

	sealed := v.aead.Seal(out[:nonceSize], nonce, plainBytes, nil)
	return sealed, nil
}

// Decrypt decrypts AES-256-GCM ciphertext created by Encrypt.
// Empty ciphertext returns "", nil.
func (v *Vault) Decrypt(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	if v == nil || v.aead == nil {
		return "", errors.New("vault is not initialized")
	}

	nonceSize := v.aead.NonceSize()
	if len(ciphertext) < nonceSize+v.aead.Overhead() {
		return "", errors.New("ciphertext too short")
	}

	nonce := ciphertext[:nonceSize]
	enc := ciphertext[nonceSize:]
	plain, err := v.aead.Open(nil, nonce, enc, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	defer ZeroBytes(plain)

	return string(plain), nil
}

// ZeroBytes overwrites b with zeroes to reduce secret retention in memory.
func ZeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
