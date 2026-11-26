package ssh

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// KeyAlgorithm defines supported SSH key generation algorithms.
type KeyAlgorithm string

const (
	AlgoEd25519 KeyAlgorithm = "ed25519"
	AlgoRSA4096 KeyAlgorithm = "rsa4096"
)

// GenerateKey generates a new OpenSSH private/public keypair in outputDir
// without overwriting any existing key file.
func GenerateKey(outputDir, filename, comment string, algo KeyAlgorithm, passphrase string) (privPath, pubPath, pubKeyStr string, err error) {
	filename = sanitizeKeyFilename(filename)
	if filename == "" {
		return "", "", "", errors.New("key filename is required")
	}

	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return "", "", "", fmt.Errorf("create output directory: %w", err)
	}

	privPath = filepath.Join(outputDir, filename)
	pubPath = privPath + ".pub"

	if _, err := os.Stat(privPath); err == nil {
		return "", "", "", fmt.Errorf("key file already exists: %s", privPath)
	}
	if _, err := os.Stat(pubPath); err == nil {
		return "", "", "", fmt.Errorf("public key file already exists: %s", pubPath)
	}

	var privKey crypto.PrivateKey
	var pubKey crypto.PublicKey

	switch algo {
	case AlgoEd25519, "":
		pub, priv, genErr := ed25519.GenerateKey(rand.Reader)
		if genErr != nil {
			return "", "", "", fmt.Errorf("generate ed25519 key: %w", genErr)
		}
		privKey = priv
		pubKey = pub
	case AlgoRSA4096:
		priv, genErr := rsa.GenerateKey(rand.Reader, 4096)
		if genErr != nil {
			return "", "", "", fmt.Errorf("generate rsa-4096 key: %w", genErr)
		}
		privKey = priv
		pubKey = &priv.PublicKey
	default:
		return "", "", "", fmt.Errorf("unsupported algorithm %q", algo)
	}

	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return "", "", "", fmt.Errorf("convert ssh public key: %w", err)
	}

	comment = strings.TrimSpace(comment)
	if comment == "" {
		comment = filename + "@ez-ssh"
	}

	var pemBlock *pem.Block
	if passphrase != "" {
		pemBlock, err = ssh.MarshalPrivateKeyWithPassphrase(privKey, comment, []byte(passphrase))
	} else {
		pemBlock, err = ssh.MarshalPrivateKey(privKey, comment)
	}
	if err != nil {
		return "", "", "", fmt.Errorf("marshal private key: %w", err)
	}

	privBytes := pem.EncodeToMemory(pemBlock)
	pubKeyStr = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment

	f, err := os.OpenFile(privPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", "", fmt.Errorf("create private key file: %w", err)
	}
	if _, werr := f.Write(privBytes); werr != nil {
		_ = f.Close()
		_ = os.Remove(privPath)
		return "", "", "", fmt.Errorf("write private key: %w", werr)
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		_ = os.Remove(privPath)
		return "", "", "", fmt.Errorf("sync private key: %w", serr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(privPath)
		return "", "", "", fmt.Errorf("close private key: %w", cerr)
	}
	if err := os.Chmod(privPath, 0o600); err != nil {
		_ = os.Remove(privPath)
		return "", "", "", fmt.Errorf("chmod private key: %w", err)
	}

	if err := os.WriteFile(pubPath, []byte(pubKeyStr+"\n"), 0o644); err != nil {
		_ = os.Remove(privPath)
		return "", "", "", fmt.Errorf("write public key: %w", err)
	}

	return privPath, pubPath, pubKeyStr, nil
}

// ReadPublicKeyFromPrivate returns the OpenSSH public key line for privPath,
// checking "<privPath>.pub" first and falling back to parsing the private key.
func ReadPublicKeyFromPrivate(privPath, passphrase string) (string, error) {
	pubPath := privPath + ".pub"
	if data, err := os.ReadFile(pubPath); err == nil {
		trimmed := strings.TrimSpace(string(data))
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(trimmed)); err == nil {
			return trimmed, nil
		}
	}

	raw, err := os.ReadFile(privPath)
	if err != nil {
		return "", fmt.Errorf("read private key: %w", err)
	}

	var signer ssh.Signer
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(raw, []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(raw)
	}
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}

	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}

func sanitizeKeyFilename(name string) string {
	name = strings.TrimSpace(name)
	name = filepath.Base(name)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), ".")
}
