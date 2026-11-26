package ssh

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
)

type testSSHServer struct {
	addr       string
	host       string
	port       int
	hostPubKey ssh.PublicKey
	allowedPub ssh.PublicKey
	sftpRoot   string
	listener   net.Listener
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
}

func startTestSSHServer(t *testing.T, allowedPub ssh.PublicKey) *testSSHServer {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate server host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer from key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("new ssh pubkey: %v", err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "testuser" && string(pass) == "secret-pw" {
				return nil, nil
			}
			return nil, errors.New("invalid password")
		},
		KeyboardInteractiveCallback: func(c ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			if c.User() != "kbuser" {
				return nil, errors.New("not kbuser")
			}
			answers, err := client("auth", "", []string{"Password: "}, []bool{false})
			if err != nil || len(answers) != 1 || answers[0] != "kb-secret" {
				return nil, errors.New("invalid kb password")
			}
			return nil, nil
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if allowedPub != nil && bytes.Equal(key.Marshal(), allowedPub.Marshal()) {
				return &ssh.Permissions{Extensions: map[string]string{"pubkey-fp": ssh.FingerprintSHA256(key)}}, nil
			}
			return nil, errors.New("unknown public key")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ssh test server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	srv := &testSSHServer{
		addr:       ln.Addr().String(),
		host:       "127.0.0.1",
		port:       ln.Addr().(*net.TCPAddr).Port,
		hostPubKey: sshPub,
		allowedPub: allowedPub,
		listener:   ln,
		ctx:        ctx,
		cancel:     cancel,
	}

	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			srv.wg.Add(1)
			go func(conn net.Conn) {
				defer srv.wg.Done()
				srv.handleConn(conn, cfg)
			}(c)
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
		srv.wg.Wait()
	})
	return srv
}

func (s *testSSHServer) handleConn(netConn net.Conn, cfg *ssh.ServerConfig) {
	defer netConn.Close()
	sConn, chans, reqs, err := ssh.NewServerConn(netConn, cfg)
	if err != nil {
		return
	}
	defer sConn.Close()

	var fwdListeners []net.Listener
	defer func() {
		for _, fl := range fwdListeners {
			_ = fl.Close()
		}
	}()

	// Global requests (keepalive & tcpip-forward)
	go func() {
		for req := range reqs {
			switch req.Type {
			case "tcpip-forward":
				var payload struct {
					Addr string
					Port uint32
				}
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					_ = req.Reply(false, nil)
					continue
				}
				fl, err := net.Listen("tcp", net.JoinHostPort(payload.Addr, strconv.Itoa(int(payload.Port))))
				if err != nil {
					_ = req.Reply(false, nil)
					continue
				}
				fwdListeners = append(fwdListeners, fl)
				boundPort := uint32(fl.Addr().(*net.TCPAddr).Port)
				_ = req.Reply(true, ssh.Marshal(struct{ Port uint32 }{boundPort}))

				go func(listener net.Listener, origAddr string, origPort uint32) {
					for {
						rc, err := listener.Accept()
						if err != nil {
							return
						}
						go func(accepted net.Conn) {
							defer accepted.Close()
							ra := accepted.RemoteAddr().(*net.TCPAddr)
							fwdMsg := struct {
								DestAddr   string
								DestPort   uint32
								OriginAddr string
								OriginPort uint32
							}{
								DestAddr:   origAddr,
								DestPort:   origPort,
								OriginAddr: ra.IP.String(),
								OriginPort: uint32(ra.Port),
							}
							ch, reqs, err := sConn.OpenChannel("forwarded-tcpip", ssh.Marshal(fwdMsg))
							if err != nil {
								return
							}
							go ssh.DiscardRequests(reqs)
							relayChannelConn(ch, accepted)
						}(rc)
					}
				}(fl, payload.Addr, boundPort)

			default:
				if req.WantReply {
					_ = req.Reply(true, nil)
				}
			}
		}
	}()

	for newCh := range chans {
		switch newCh.ChannelType() {
		case "session":
			ch, requests, err := newCh.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer ch.Close()
				for req := range requests {
					switch req.Type {
					case "exec":
						var cmdMsg struct {
							Command string
						}
						_ = ssh.Unmarshal(req.Payload, &cmdMsg)
						_ = req.Reply(true, nil)
						_, _ = io.WriteString(ch, "exec:"+cmdMsg.Command+"\n")
						_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						return
					case "pty-req", "window-change":
						if req.WantReply {
							_ = req.Reply(true, nil)
						}
					case "shell":
						if req.WantReply {
							_ = req.Reply(true, nil)
						}
						go func() {
							_, _ = io.WriteString(ch, "\x1b[32mREADY\x1b[0m\r\n")
							sc := bufio.NewScanner(ch)
							sc.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
								for i, b := range data {
									if b == '\r' || b == '\n' {
										return i + 1, data[:i], nil
									}
								}
								if atEOF && len(data) > 0 {
									return len(data), data, nil
								}
								return 0, nil, nil
							})
							for sc.Scan() {
								line := strings.TrimSpace(sc.Text())
								if line == "" {
									continue
								}
								switch line {
								case "spam":
									for i := 0; i < 2500; i++ {
										_, _ = fmt.Fprintf(ch, "line-%04d\r\n", i)
									}
								case "exit":
									_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
									_ = ch.Close()
									return
								default:
									_, _ = io.WriteString(ch, "echo:"+line+"\r\n")
								}
							}
						}()
					case "subsystem":
						var subMsg struct {
							Name string
						}
						_ = ssh.Unmarshal(req.Payload, &subMsg)
						if subMsg.Name == "sftp" {
							_ = req.Reply(true, nil)
							var opts []sftp.ServerOption
							if s.sftpRoot != "" {
								opts = append(opts, sftp.WithServerWorkingDirectory(s.sftpRoot))
							}
							server, serr := sftp.NewServer(ch, opts...)
							if serr == nil {
								go func() {
									defer server.Close()
									_ = server.Serve()
								}()
							}
						} else if req.WantReply {
							_ = req.Reply(false, nil)
						}
					default:
						if req.WantReply {
							_ = req.Reply(false, nil)
						}
					}
				}
			}()
		case "direct-tcpip":
			var dMsg struct {
				DestAddr   string
				DestPort   uint32
				OriginAddr string
				OriginPort uint32
			}
			if err := ssh.Unmarshal(newCh.ExtraData(), &dMsg); err != nil {
				_ = newCh.Reject(ssh.Prohibited, "bad payload")
				continue
			}
			targetConn, err := net.DialTimeout("tcp", net.JoinHostPort(dMsg.DestAddr, strconv.Itoa(int(dMsg.DestPort))), 3*time.Second)
			if err != nil {
				_ = newCh.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			ch, reqs, err := newCh.Accept()
			if err != nil {
				_ = targetConn.Close()
				continue
			}
			go ssh.DiscardRequests(reqs)
			go relayChannelConn(ch, targetConn)
		default:
			_ = newCh.Reject(ssh.UnknownChannelType, "unknown channel type")
		}
	}
}

func relayChannelConn(ch ssh.Channel, c net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(ch, c)
		_ = ch.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(c, ch)
		if tc, ok := c.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
	}()
	wg.Wait()
	_ = ch.Close()
	_ = c.Close()
}

func startEchoServer(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo server: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func TestKeygenAndPublicKeyRead(t *testing.T) {
	dir := t.TempDir()

	priv, pub, pubStr, err := GenerateKey(dir, "id_test_ed", "me@test", AlgoEd25519, "pass-123")
	if err != nil {
		t.Fatalf("GenerateKey(ed25519) error = %v", err)
	}
	if !strings.HasPrefix(pubStr, "ssh-ed25519 ") {
		t.Fatalf("unexpected pubStr = %q", pubStr)
	}
	info, err := os.Stat(priv)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private key stat/perm = (%v, %v)", info, err)
	}

	// Refuse overwrite
	if _, _, _, err := GenerateKey(dir, "id_test_ed", "", AlgoEd25519, ""); err == nil {
		t.Fatal("expected GenerateKey to refuse overwriting existing key")
	}

	// Remove .pub and verify derivation from encrypted private key
	_ = os.Remove(pub)
	derived, err := ReadPublicKeyFromPrivate(priv, "pass-123")
	if err != nil {
		t.Fatalf("ReadPublicKeyFromPrivate error = %v", err)
	}
	if !strings.HasPrefix(derived, "ssh-ed25519 ") {
		t.Fatalf("derived pubkey = %q", derived)
	}

	// RSA 4096
	_, _, rsaPub, err := GenerateKey(dir, "id_test_rsa", "", AlgoRSA4096, "")
	if err != nil {
		t.Fatalf("GenerateKey(rsa4096) error = %v", err)
	}
	if !strings.HasPrefix(rsaPub, "ssh-rsa ") {
		t.Fatalf("unexpected rsaPub = %q", rsaPub)
	}
}

func TestKnownHostsParseAndAtomicDelete(t *testing.T) {
	dir := t.TempDir()
	khPath := filepath.Join(dir, "known_hosts")

	_, priv1, _ := ed25519.GenerateKey(rand.Reader)
	s1, _ := ssh.NewSignerFromKey(priv1)
	_, priv2, _ := ed25519.GenerateKey(rand.Reader)
	s2, _ := ssh.NewSignerFromKey(priv2)

	if err := AppendKnownHost(khPath, "10.0.0.1:22", nil, s1.PublicKey()); err != nil {
		t.Fatalf("AppendKnownHost 1 error = %v", err)
	}
	if err := AppendKnownHost(khPath, "10.0.0.2:2222", nil, s2.PublicKey()); err != nil {
		t.Fatalf("AppendKnownHost 2 error = %v", err)
	}

	entries, err := ParseKnownHosts(khPath)
	if err != nil {
		t.Fatalf("ParseKnownHosts error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(entries))
	}

	if err := DeleteKnownHostEntry(khPath, entries[0]); err != nil {
		t.Fatalf("DeleteKnownHostEntry error = %v", err)
	}
	if _, err := os.Stat(khPath + ".bak"); err != nil {
		t.Fatalf("expected backup file %s.bak: %v", khPath, err)
	}

	after, err := ParseKnownHosts(khPath)
	if err != nil {
		t.Fatalf("ParseKnownHosts after delete error = %v", err)
	}
	if len(after) != 1 || after[0].Hosts != "[10.0.0.2]:2222" {
		t.Fatalf("remaining entries = %+v", after)
	}
}

func TestParseSSHConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	content := `
# Global defaults
Host *
    User globaluser
    Port 2222
    IdentityFile ~/.ssh/id_global

Host prod-web
    HostName 10.0.1.12
    User ubuntu

Host staging-*
    HostName 10.0.2.10

Match host *.internal
    User matchuser

Host db-01
    HostName=10.0.1.15
    Port=5432
    IdentityFile /tmp/custom_key
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write ssh config: %v", err)
	}

	hosts, err := ParseSSHConfig(cfgPath)
	if err != nil {
		t.Fatalf("ParseSSHConfig error = %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("parsed hosts len = %d, want 2 (%+v)", len(hosts), hosts)
	}
	if hosts[0].Name != "prod-web" || hosts[0].Port != 2222 || hosts[0].Username != "ubuntu" {
		t.Fatalf("host[0] = %+v", hosts[0])
	}
	if hosts[1].Name != "db-01" || hosts[1].Hostname != "10.0.1.15" || hosts[1].Port != 5432 || hosts[1].Username != "globaluser" || hosts[1].IdentityFile != "/tmp/custom_key" {
		t.Fatalf("host[1] = %+v", hosts[1])
	}
}

func TestAskpassServerAndShellQuote(t *testing.T) {
	runDir := t.TempDir()
	srv, err := NewAskpassServer(runDir, "top-secret-pw")
	if err != nil {
		t.Fatalf("NewAskpassServer error = %v", err)
	}
	defer srv.Close()

	// First fetch succeeds.
	got, ok := fetchAskpassSecret(srv.sockPath)
	if !ok || got != "top-secret-pw" {
		t.Fatalf("first fetchAskpassSecret = (%q, %v)", got, ok)
	}
	// Second fetch is refused (single-use).
	if got2, ok2 := fetchAskpassSecret(srv.sockPath); ok2 {
		t.Fatalf("second fetchAskpassSecret unexpectedly returned (%q, %v)", got2, ok2)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("srv.Close error = %v", err)
	}
	if _, err := os.Stat(srv.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected askpass dir removed, stat err = %v", err)
	}

	quoted := ShellQuote("a'b;rm -rf /")
	if quoted != "'a'\\''b;rm -rf /'" {
		t.Fatalf("ShellQuote = %q", quoted)
	}
}

func TestDialHostSnippetAndAllTunnelsNoGoroutineLeak(t *testing.T) {
	tmp := t.TempDir()
	vault, err := crypto.NewVault(filepath.Join(tmp, ".vault.key"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	khPath := filepath.Join(tmp, "known_hosts")

	// Generate encrypted client key
	privPath, _, pubStr, err := GenerateKey(tmp, "client_ed25519", "", AlgoEd25519, "keypass")
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	allowedPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubStr))
	if err != nil {
		t.Fatalf("ParseAuthorizedKey: %v", err)
	}

	srv := startTestSSHServer(t, allowedPub)

	// 1. Password auth + TOFU
	pwEnc, _ := vault.Encrypt("secret-pw")
	pwHost := db.Host{
		ID:              1,
		Name:            "pw-host",
		Hostname:        srv.host,
		Port:            srv.port,
		Username:        "testuser",
		InlineSecretEnc: pwEnc,
	}
	out, _, err := ExecuteSnippet(context.Background(), pwHost, nil, vault, khPath, "uptime")
	if err != nil {
		t.Fatalf("ExecuteSnippet(password) error = %v", err)
	}
	if strings.TrimSpace(out) != "exec:uptime" {
		t.Fatalf("snippet output = %q", out)
	}

	// 2. Host key mismatch MUST fail!
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(otherPriv)
	badKH := filepath.Join(tmp, "bad_known_hosts")
	_ = AppendKnownHost(badKH, net.JoinHostPort(srv.host, strconv.Itoa(srv.port)), nil, otherSigner.PublicKey())
	if _, _, err := ExecuteSnippet(context.Background(), pwHost, nil, vault, badKH, "uptime"); err == nil {
		t.Fatal("expected DialHost to reject mismatched host key")
	}

	// 3. Keyboard-interactive auth
	kbEnc, _ := vault.Encrypt("kb-secret")
	kbHost := db.Host{ID: 2, Name: "kb-host", Hostname: srv.host, Port: srv.port, Username: "kbuser", InlineSecretEnc: kbEnc}
	if out, _, err := ExecuteSnippet(context.Background(), kbHost, nil, vault, khPath, "whoami"); err != nil || strings.TrimSpace(out) != "exec:whoami" {
		t.Fatalf("ExecuteSnippet(kb-interactive) = (%q, %v)", out, err)
	}

	// 4. Keychain encrypted private key auth
	keyPassEnc, _ := vault.Encrypt("keypass")
	kc := &db.KeychainItem{
		ID:             1,
		Name:           "client-key",
		Type:           db.KeychainSSHKey,
		PrivateKeyPath: privPath,
		SecretEnc:      keyPassEnc,
	}
	keyHost := db.Host{ID: 3, Name: "key-host", Hostname: srv.host, Port: srv.port, Username: "keyuser", KeychainID: &kc.ID}

	// Baseline goroutines before tunnel test
	runtime.GC()
	baseGoroutines := runtime.NumGoroutine()

	echoHost, echoPort := startEchoServer(t)
	mgr := NewTunnelManager(vault, khPath)

	// Pick free local ports
	freePort := func() int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		return p
	}

	localPort := freePort()
	socksPort := freePort()
	remoteBindPort := freePort()

	// Local -L tunnel
	pfLocal := db.PortForward{
		ID: 10, Name: "local-echo", HostID: keyHost.ID, Type: db.TunnelLocal,
		LocalAddr: "127.0.0.1", LocalPort: localPort, RemoteAddr: echoHost, RemotePort: echoPort,
	}
	if err := mgr.Start(pfLocal, keyHost, kc); err != nil {
		t.Fatalf("Start local tunnel: %v", err)
	}

	// Dynamic -D SOCKS5 tunnel
	pfSocks := db.PortForward{
		ID: 11, Name: "socks5", HostID: keyHost.ID, Type: db.TunnelDynamic,
		LocalAddr: "127.0.0.1", LocalPort: socksPort,
	}
	if err := mgr.Start(pfSocks, keyHost, kc); err != nil {
		t.Fatalf("Start dynamic tunnel: %v", err)
	}

	// Remote -R tunnel
	pfRemote := db.PortForward{
		ID: 12, Name: "remote-echo", HostID: keyHost.ID, Type: db.TunnelRemote,
		LocalAddr: echoHost, LocalPort: echoPort, RemoteAddr: "127.0.0.1", RemotePort: remoteBindPort,
	}
	if err := mgr.Start(pfRemote, keyHost, kc); err != nil {
		t.Fatalf("Start remote tunnel: %v", err)
	}

	waitTunnelActive := func(id int64) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			st := mgr.Status(id)
			if st.State == TunnelActive {
				return
			}
			if st.State == TunnelError {
				t.Fatalf("tunnel %d entered error state: %s", id, st.Error)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("tunnel %d timed out waiting for active state (%+v)", id, mgr.Status(id))
	}
	waitTunnelActive(10)
	waitTunnelActive(11)
	waitTunnelActive(12)

	// Verify Local -L data relay
	cL, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial local tunnel: %v", err)
	}
	_, _ = io.WriteString(cL, "ping-local\n")
	lineL, err := bufio.NewReader(cL).ReadString('\n')
	_ = cL.Close()
	if err != nil || lineL != "ping-local\n" {
		t.Fatalf("local tunnel echo = (%q, %v)", lineL, err)
	}

	// Verify Dynamic -D SOCKS5 data relay
	cS, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial socks5 tunnel: %v", err)
	}
	_, _ = cS.Write([]byte{0x05, 0x01, 0x00})
	var authResp [2]byte
	if _, err := io.ReadFull(cS, authResp[:]); err != nil || authResp != [2]byte{0x05, 0x00} {
		t.Fatalf("socks5 auth resp = (%v, %v)", authResp, err)
	}
	req := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, byte(echoPort >> 8), byte(echoPort & 0xff)}
	_, _ = cS.Write(req)
	var rep [10]byte
	if _, err := io.ReadFull(cS, rep[:]); err != nil || rep[1] != 0x00 {
		t.Fatalf("socks5 connect resp = (%v, %v)", rep, err)
	}
	_, _ = io.WriteString(cS, "ping-socks\n")
	lineS, err := bufio.NewReader(cS).ReadString('\n')
	_ = cS.Close()
	if err != nil || lineS != "ping-socks\n" {
		t.Fatalf("socks5 echo = (%q, %v)", lineS, err)
	}

	// Verify Remote -R data relay
	cR, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(remoteBindPort)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial remote tunnel: %v", err)
	}
	_, _ = io.WriteString(cR, "ping-remote\n")
	lineR, err := bufio.NewReader(cR).ReadString('\n')
	_ = cR.Close()
	if err != nil || lineR != "ping-remote\n" {
		t.Fatalf("remote tunnel echo = (%q, %v)", lineR, err)
	}

	// Verify clean shutdown & no goroutine leak
	mgr.ShutdownAll()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= baseGoroutines+2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if cur := runtime.NumGoroutine(); cur > baseGoroutines+2 {
		t.Fatalf("potential goroutine leak: before=%d after=%d", baseGoroutines, cur)
	}
	// Avoid unused binary warning if any
	_ = binary.BigEndian
	_ = fmt.Sprint()
}

func TestBuildInteractiveCmdNoSecretLeakAndRecording(t *testing.T) {
	tmp := t.TempDir()
	vault, err := crypto.NewVault(filepath.Join(tmp, ".vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(tmp, "run")
	logsDir := filepath.Join(tmp, "logs")

	pw := "UltraSecretPassword!99"
	enc, err := vault.Encrypt(pw)
	if err != nil {
		t.Fatal(err)
	}
	host := db.Host{
		ID:              42,
		Name:            "prod-db",
		Hostname:        "10.0.1.9",
		Port:            2222,
		Username:        "admin",
		InlineSecretEnc: enc,
		RecordSession:   true,
	}

	cmd, askpass, transcriptPath, err := BuildInteractiveCmd(host, nil, vault, runDir, logsDir)
	if err != nil {
		t.Fatalf("BuildInteractiveCmd error = %v", err)
	}
	if askpass == nil {
		t.Fatal("expected AskpassServer to be created")
	}
	defer askpass.Close()

	// Ensure plaintext password never appears in CLI args or env vars!
	for _, arg := range cmd.Args {
		if strings.Contains(arg, pw) {
			t.Fatalf("password leaked into CLI arg: %q", arg)
		}
	}
	for _, env := range cmd.Env {
		if strings.Contains(env, pw) {
			t.Fatalf("password leaked into env var: %q", env)
		}
	}

	if transcriptPath == "" {
		t.Fatal("expected transcriptPath when RecordSession=true and script binary exists")
	}
	info, err := os.Stat(transcriptPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("transcript stat/perm = (%v, %v)", info, err)
	}
}
