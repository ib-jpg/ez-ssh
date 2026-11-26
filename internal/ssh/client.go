package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	xknownhosts "golang.org/x/crypto/ssh/knownhosts"

	"github.com/ib-jpg/ez-ssh/internal/config"
	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
)

// DialHost establishes an authenticated SSH client connection respecting ctx,
// Vault-encrypted credentials, SSH agent fallback, and strict known_hosts checks.
func DialHost(
	ctx context.Context,
	host db.Host,
	kc *db.KeychainItem,
	vault *crypto.Vault,
	knownHostsPath string,
) (*ssh.Client, error) {
	return DialHostWithProgress(ctx, host, kc, vault, knownHostsPath, "", nil)
}

// DialHostWithProgress dials an SSH host while reporting handshake stages via onStep.
// If overrideSecret is non-empty, it is used instead of (or in addition to) stored credentials.
func DialHostWithProgress(
	ctx context.Context,
	host db.Host,
	kc *db.KeychainItem,
	vault *crypto.Vault,
	knownHostsPath string,
	overrideSecret string,
	onStep func(string),
) (*ssh.Client, error) {
	report := func(step string) {
		if onStep != nil {
			onStep(step)
		}
	}

	username := strings.TrimSpace(host.Username)
	if username == "" && kc != nil {
		username = strings.TrimSpace(kc.Username)
	}
	if username == "" {
		return nil, errors.New("ssh username is empty")
	}

	port := host.Port
	if port <= 0 {
		port = 22
	}
	addr := net.JoinHostPort(host.Hostname, strconv.Itoa(port))

	authMethods, agentConn, err := buildAuthMethods(host, kc, vault, overrideSecret)
	if err != nil {
		return nil, err
	}
	if agentConn != nil {
		defer agentConn.Close()
	}
	if len(authMethods) == 0 {
		return nil, errors.New("no SSH authentication method available (configure Keychain, password, or SSH agent)")
	}

	hostKeyCb := buildHostKeyCallback(knownHostsPath, addr)

	cfg := &ssh.ClientConfig{
		User:            username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCb,
		Timeout:         15 * time.Second,
	}

	report("TCP Handshake")
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	netConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", addr, err)
	}

	// Ensure context cancellation aborts an in-flight SSH handshake without leaking a goroutine.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = netConn.Close()
		case <-done:
		}
	}()

	report("SSH Auth")
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, addr, cfg)
	close(done)
	if err != nil {
		_ = netConn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ssh handshake %s: %w", addr, err)
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}

// ClassifySSHError separates SSH authentication failures from network/transport/host-key errors
// and returns a concise, user-friendly summary.
func ClassifySSHError(err error) (isAuth bool, summary string) {
	if err == nil {
		return false, ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)

	if strings.Contains(msg, "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		return false, msg
	}
	if strings.Contains(lower, "unable to authenticate") ||
		strings.Contains(lower, "no supported methods remain") ||
		strings.Contains(lower, "passphrase") ||
		strings.Contains(lower, "no ssh authentication method") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "unexpected message type 51") {
		return true, "Incorrect password or rejected SSH credential"
	}
	return false, msg
}

func buildAuthMethods(host db.Host, kc *db.KeychainItem, vault *crypto.Vault, overrideSecret string) ([]ssh.AuthMethod, net.Conn, error) {
	var methods []ssh.AuthMethod

	if overrideSecret != "" {
		if kc != nil && kc.Type == db.KeychainSSHKey {
			if signer, err := loadSignerRaw(config.ExpandPath(kc.PrivateKeyPath), overrideSecret); err == nil {
				methods = append(methods, ssh.PublicKeys(signer))
			}
		}
		methods = appendPasswordAuthMethods(methods, overrideSecret)
		return methods, nil, nil
	}

	if kc != nil {
		switch kc.Type {
		case db.KeychainSSHKey:
			signer, err := loadSignerFromPath(config.ExpandPath(kc.PrivateKeyPath), kc.SecretEnc, vault)
			if err != nil {
				return nil, nil, err
			}
			methods = append(methods, ssh.PublicKeys(signer))
		case db.KeychainPassword:
			if len(kc.SecretEnc) > 0 && vault != nil {
				pw, err := vault.Decrypt(kc.SecretEnc)
				if err != nil {
					return nil, nil, fmt.Errorf("decrypt keychain password: %w", err)
				}
				if pw != "" {
					methods = appendPasswordAuthMethods(methods, pw)
				}
			}
		}
	}

	if len(host.InlineSecretEnc) > 0 && vault != nil {
		pw, err := vault.Decrypt(host.InlineSecretEnc)
		if err != nil {
			return nil, nil, fmt.Errorf("decrypt host inline password: %w", err)
		}
		if pw != "" {
			methods = appendPasswordAuthMethods(methods, pw)
		}
	}

	var agentConn net.Conn
	if sock := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")); sock != "" {
		if c, err := net.DialTimeout("unix", sock, 2*time.Second); err == nil {
			agentConn = c
			ag := agent.NewClient(c)
			methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
		}
	}

	// Fallback to standard default user keys if no explicit Keychain/inline credential is set.
	if kc == nil && len(host.InlineSecretEnc) == 0 {
		if home, err := os.UserHomeDir(); err == nil {
			for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
				p := filepath.Join(home, ".ssh", name)
				if signer, err := loadSignerFromPath(p, nil, nil); err == nil {
					methods = append(methods, ssh.PublicKeys(signer))
				}
			}
		}
	}

	return methods, agentConn, nil
}

func loadSignerRaw(path, passphrase string) (ssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if passphrase != "" {
		pb := []byte(passphrase)
		defer crypto.ZeroBytes(pb)
		return ssh.ParsePrivateKeyWithPassphrase(raw, pb)
	}
	return ssh.ParsePrivateKey(raw)
}

func appendPasswordAuthMethods(methods []ssh.AuthMethod, pw string) []ssh.AuthMethod {
	methods = append(methods, ssh.Password(pw))
	methods = append(methods, ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range questions {
			answers[i] = pw
		}
		return answers, nil
	}))
	return methods
}

func loadSignerFromPath(path string, passEnc []byte, vault *crypto.Vault) (ssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key %s: %w", path, err)
	}
	if len(passEnc) > 0 && vault != nil {
		passphrase, err := vault.Decrypt(passEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt key passphrase: %w", err)
		}
		passBytes := []byte(passphrase)
		defer crypto.ZeroBytes(passBytes)
		if len(passBytes) > 0 {
			signer, err := ssh.ParsePrivateKeyWithPassphrase(raw, passBytes)
			if err != nil {
				return nil, fmt.Errorf("parse encrypted private key %s: %w", path, err)
			}
			return signer, nil
		}
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse private key %s: %w", path, err)
	}
	return signer, nil
}

func buildHostKeyCallback(knownHostsPath, hostPort string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if strings.TrimSpace(knownHostsPath) == "" {
			return errors.New("known_hosts path is not configured")
		}

		// If known_hosts does not exist yet, trust-on-first-use and create it.
		if _, err := os.Stat(knownHostsPath); errors.Is(err, os.ErrNotExist) {
			return AppendKnownHost(knownHostsPath, hostPort, remote, key)
		}

		cb, err := xknownhosts.New(knownHostsPath)
		if err != nil {
			return fmt.Errorf("load known_hosts %s: %w", knownHostsPath, err)
		}

		err = cb(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *xknownhosts.KeyError
		if errors.As(err, &keyErr) {
			if len(keyErr.Want) > 0 {
				return fmt.Errorf(
					"REMOTE HOST IDENTIFICATION HAS CHANGED for %s (got %s) — remove old entry in Known Hosts tab",
					hostname, ssh.FingerprintSHA256(key),
				)
			}
			// Host not yet present in known_hosts -> TOFU accept & persist.
			return AppendKnownHost(knownHostsPath, hostPort, remote, key)
		}

		return err
	}
}
