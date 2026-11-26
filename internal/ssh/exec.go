package ssh

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/ib-jpg/ez-ssh/internal/config"
	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
)

const askpassSockEnv = "EZ_SSH_ASKPASS_SOCK"

// AskpassServer serves a single-use secret over a 0600 Unix domain socket
// for OpenSSH SSH_ASKPASS without exposing passwords in CLI args or env vars.
type AskpassServer struct {
	dir        string
	sockPath   string
	scriptPath string
	listener   net.Listener
	secret     []byte
	served     atomic.Bool
	wg         sync.WaitGroup
	closeOnce  sync.Once
}

// NewAskpassServer initializes an ephemeral askpass Unix socket and wrapper script inside runDir.
func NewAskpassServer(runDir, secret string) (*AskpassServer, error) {
	if secret == "" {
		return nil, errors.New("empty secret for askpass")
	}
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, fmt.Errorf("create runDir: %w", err)
	}

	dir, err := os.MkdirTemp(runDir, "askpass-*")
	if err != nil {
		return nil, fmt.Errorf("create askpass temp dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("chmod askpass dir: %w", err)
	}

	sockPath := filepath.Join(dir, "askpass.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("listen askpass socket: %w", err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("chmod askpass socket: %w", err)
	}

	exe, err := os.Executable()
	if err != nil {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}

	scriptPath := filepath.Join(dir, "askpass.sh")
	scriptBody := fmt.Sprintf("#!/bin/sh\nexec %s --internal-askpass \"$@\"\n", ShellQuote(exe))
	if err := os.WriteFile(scriptPath, []byte(scriptBody), 0o700); err != nil {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write askpass script: %w", err)
	}

	srv := &AskpassServer{
		dir:        dir,
		sockPath:   sockPath,
		scriptPath: scriptPath,
		listener:   ln,
		secret:     []byte(secret),
	}

	srv.wg.Add(1)
	go srv.acceptLoop()
	return srv, nil
}

func (s *AskpassServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		func(c net.Conn) {
			defer c.Close()
			_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if !s.served.Swap(true) && len(s.secret) > 0 {
				_, _ = c.Write(s.secret)
				_, _ = c.Write([]byte{'\n'})
			}
		}(conn)
	}
}

// Env returns environment variables needed for OpenSSH to invoke this askpass server.
func (s *AskpassServer) Env() []string {
	return []string{
		"SSH_ASKPASS=" + s.scriptPath,
		"SSH_ASKPASS_REQUIRE=force",
		"DISPLAY=ez-ssh:0",
		askpassSockEnv + "=" + s.sockPath,
	}
}

// Close stops the Unix socket server, zeroes the secret in memory, and removes temp files.
func (s *AskpassServer) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		closeErr = s.listener.Close()
		s.wg.Wait()
		crypto.ZeroBytes(s.secret)
		s.secret = nil
		if err := os.RemoveAll(s.dir); err != nil && closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}

// RunInternalAskpass implements the `--internal-askpass <prompt>` helper invoked by OpenSSH.
func RunInternalAskpass(args []string) int {
	prompt := strings.TrimSpace(strings.Join(args, " "))
	lower := strings.ToLower(prompt)

	// Host key authenticity and confirmation prompts must be answered interactively by the user.
	if isInteractiveConfirmPrompt(lower) {
		ans, err := readLineFromTTY(prompt)
		if err != nil {
			return 1
		}
		_, _ = fmt.Fprintln(os.Stdout, ans)
		return 0
	}

	if sockPath := os.Getenv(askpassSockEnv); sockPath != "" {
		if secret, ok := fetchAskpassSecret(sockPath); ok {
			_, _ = fmt.Fprintln(os.Stdout, secret)
			return 0
		}
	}

	// Fallback to /dev/tty password input (e.g. if password was rejected and SSH retries).
	secret, err := readSecretFromTTY(prompt)
	if err != nil {
		return 1
	}
	_, _ = fmt.Fprintln(os.Stdout, secret)
	return 0
}

func isInteractiveConfirmPrompt(lowerPrompt string) bool {
	return strings.Contains(lowerPrompt, "continue connecting") ||
		strings.Contains(lowerPrompt, "yes/no") ||
		strings.Contains(lowerPrompt, "fingerprint") ||
		strings.Contains(lowerPrompt, "are you sure")
}

func fetchAskpassSecret(sockPath string) (string, bool) {
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	data, err := io.ReadAll(io.LimitReader(conn, 8192))
	if err != nil {
		return "", false
	}
	line := strings.TrimRight(string(data), "\r\n")
	if line == "" {
		return "", false
	}
	return line, true
}

func readLineFromTTY(prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer tty.Close()

	if prompt != "" {
		_, _ = fmt.Fprint(tty, prompt+" ")
	}
	reader := bufio.NewReader(tty)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func readSecretFromTTY(prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer tty.Close()

	if prompt != "" {
		_, _ = fmt.Fprint(tty, prompt+" ")
	}
	b, err := term.ReadPassword(int(tty.Fd()))
	_, _ = fmt.Fprintln(tty)
	if err != nil {
		return "", err
	}
	defer crypto.ZeroBytes(b)
	return string(b), nil
}

// BuildInteractiveCmd constructs an *exec.Cmd suitable for tea.ExecProcess to run
// an interactive native ssh session, optionally recording output via script(1) and
// securely providing passwords/passphrases via AskpassServer.
func BuildInteractiveCmd(
	host db.Host,
	kc *db.KeychainItem,
	vault *crypto.Vault,
	runDir string,
	logsDir string,
) (*exec.Cmd, *AskpassServer, string, error) {
	port := host.Port
	if port <= 0 {
		port = 22
	}
	username := strings.TrimSpace(host.Username)
	if username == "" && kc != nil {
		username = strings.TrimSpace(kc.Username)
	}
	if username == "" {
		return nil, nil, "", errors.New("ssh username is empty")
	}

	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return nil, nil, "", fmt.Errorf("ssh binary not found in PATH: %w", err)
	}

	var sshArgs []string
	sshArgs = append(sshArgs, "-p", strconv.Itoa(port))

	if kc != nil && kc.Type == db.KeychainSSHKey && strings.TrimSpace(kc.PrivateKeyPath) != "" {
		keyPath := config.ExpandPath(kc.PrivateKeyPath)
		sshArgs = append(sshArgs, "-i", keyPath, "-o", "IdentitiesOnly=yes")
	}

	target := username + "@" + host.Hostname
	sshArgs = append(sshArgs, target)

	var secret string
	if kc != nil && len(kc.SecretEnc) > 0 && vault != nil {
		secret, err = vault.Decrypt(kc.SecretEnc)
		if err != nil {
			return nil, nil, "", fmt.Errorf("decrypt keychain secret: %w", err)
		}
	} else if len(host.InlineSecretEnc) > 0 && vault != nil {
		secret, err = vault.Decrypt(host.InlineSecretEnc)
		if err != nil {
			return nil, nil, "", fmt.Errorf("decrypt host secret: %w", err)
		}
	}

	var askpass *AskpassServer
	if secret != "" {
		askpass, err = NewAskpassServer(runDir, secret)
		secret = ""
		if err != nil {
			return nil, nil, "", err
		}
	}

	var cmd *exec.Cmd
	var transcriptPath string

	if host.RecordSession && strings.TrimSpace(logsDir) != "" {
		if scriptBin, lookErr := exec.LookPath("script"); lookErr == nil {
			tPath, mkErr := prepareTranscriptFile(logsDir, host.Name)
			if mkErr == nil {
				transcriptPath = tPath
				switch runtime.GOOS {
				case "darwin":
					args := append([]string{"-q", "-F", transcriptPath, sshBin}, sshArgs...)
					cmd = exec.Command(scriptBin, args...)
				default:
					quotedParts := make([]string, 0, len(sshArgs)+1)
					quotedParts = append(quotedParts, ShellQuote(sshBin))
					for _, a := range sshArgs {
						quotedParts = append(quotedParts, ShellQuote(a))
					}
					cmdStr := strings.Join(quotedParts, " ")
					cmd = exec.Command(scriptBin, "-q", "-f", "-e", "-c", cmdStr, transcriptPath)
				}
			}
		}
	}

	if cmd == nil {
		cmd = exec.Command(sshBin, sshArgs...)
	}

	env := filteredBaseEnv()
	if askpass != nil {
		env = append(env, askpass.Env()...)
	}
	cmd.Env = env
	return cmd, askpass, transcriptPath, nil
}

// BuildCopyIDCmd constructs an *exec.Cmd to install a public key onto a target host via ssh-copy-id.
func BuildCopyIDCmd(
	pubKeyPath string,
	host db.Host,
	hostKC *db.KeychainItem,
	vault *crypto.Vault,
	runDir string,
) (*exec.Cmd, *AskpassServer, error) {
	copyIDBin, err := exec.LookPath("ssh-copy-id")
	if err != nil {
		return nil, nil, fmt.Errorf("ssh-copy-id binary not found in PATH: %w", err)
	}
	port := host.Port
	if port <= 0 {
		port = 22
	}
	username := strings.TrimSpace(host.Username)
	if username == "" && hostKC != nil {
		username = strings.TrimSpace(hostKC.Username)
	}
	if username == "" {
		return nil, nil, errors.New("host username is empty")
	}

	args := []string{"-i", pubKeyPath, "-p", strconv.Itoa(port), username + "@" + host.Hostname}

	var secret string
	if hostKC != nil && len(hostKC.SecretEnc) > 0 && vault != nil {
		secret, err = vault.Decrypt(hostKC.SecretEnc)
		if err != nil {
			return nil, nil, fmt.Errorf("decrypt host keychain secret: %w", err)
		}
	} else if len(host.InlineSecretEnc) > 0 && vault != nil {
		secret, err = vault.Decrypt(host.InlineSecretEnc)
		if err != nil {
			return nil, nil, fmt.Errorf("decrypt host inline secret: %w", err)
		}
	}

	var askpass *AskpassServer
	if secret != "" {
		askpass, err = NewAskpassServer(runDir, secret)
		secret = ""
		if err != nil {
			return nil, nil, err
		}
	}

	cmd := exec.Command(copyIDBin, args...)
	env := filteredBaseEnv()
	if askpass != nil {
		env = append(env, askpass.Env()...)
	}
	cmd.Env = env
	return cmd, askpass, nil
}

func filteredBaseEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "SSH_ASKPASS=") ||
			strings.HasPrefix(e, "SSH_ASKPASS_REQUIRE=") ||
			strings.HasPrefix(e, askpassSockEnv+"=") {
			continue
		}
		env = append(env, e)
	}
	return env
}

func prepareTranscriptFile(logsDir, hostName string) (string, error) {
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		return "", err
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	safeHost := sanitizeKeyFilename(hostName)
	if safeHost == "" {
		safeHost = "session"
	}
	filename := fmt.Sprintf("%s_%s_%s.log", time.Now().Format("20060102_150405"), safeHost, hex.EncodeToString(rnd[:]))
	path := filepath.Join(logsDir, filename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	_ = f.Close()
	return path, nil
}

// ShellQuote wraps s in POSIX single quotes with safe escaping of embedded single quotes.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
