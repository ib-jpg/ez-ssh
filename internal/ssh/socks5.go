package ssh

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
)

const (
	socksVersion5 = 0x05
	socksAuthNone = 0x00
	socksAuthFail = 0xff

	socksCmdConnect = 0x01

	socksAtypIPv4   = 0x01
	socksAtypDomain = 0x03
	socksAtypIPv6   = 0x04

	socksReplySucceeded         = 0x00
	socksReplyConnectionRefused = 0x05
	socksReplyCmdNotSupported   = 0x07
	socksReplyAddrNotSupported  = 0x08
)

// ServeSOCKS5 handles a single RFC 1928 SOCKS5 CONNECT client connection and relays
// traffic using dialRemote until EOF or ctx cancellation.
func ServeSOCKS5(
	ctx context.Context,
	clientConn net.Conn,
	dialRemote func(ctx context.Context, network, addr string) (net.Conn, error),
) error {
	defer clientConn.Close()

	if err := negotiateSOCKS5Auth(clientConn); err != nil {
		return err
	}

	targetAddr, err := readSOCKS5Request(clientConn)
	if err != nil {
		return err
	}

	remoteConn, err := dialRemote(ctx, "tcp", targetAddr)
	if err != nil {
		_ = writeSOCKS5Reply(clientConn, socksReplyConnectionRefused)
		return err
	}
	defer remoteConn.Close()

	if err := writeSOCKS5Reply(clientConn, socksReplySucceeded); err != nil {
		return err
	}

	RelayBidirectional(ctx, clientConn, remoteConn)
	return nil
}

func negotiateSOCKS5Auth(conn net.Conn) error {
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return err
	}
	if hdr[0] != socksVersion5 {
		return fmt.Errorf("unsupported socks version %d", hdr[0])
	}
	nMethods := int(hdr[1])
	if nMethods == 0 {
		_, _ = conn.Write([]byte{socksVersion5, socksAuthFail})
		return errors.New("socks5: zero auth methods")
	}
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}

	supportsNone := false
	for _, m := range methods {
		if m == socksAuthNone {
			supportsNone = true
			break
		}
	}
	if !supportsNone {
		_, _ = conn.Write([]byte{socksVersion5, socksAuthFail})
		return errors.New("socks5: client does not support no-auth")
	}

	_, err := conn.Write([]byte{socksVersion5, socksAuthNone})
	return err
}

func readSOCKS5Request(conn net.Conn) (string, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return "", err
	}
	if hdr[0] != socksVersion5 {
		return "", fmt.Errorf("socks5 invalid request version %d", hdr[0])
	}
	if hdr[1] != socksCmdConnect {
		_ = writeSOCKS5Reply(conn, socksReplyCmdNotSupported)
		return "", fmt.Errorf("socks5 unsupported command %d", hdr[1])
	}

	var host string
	switch hdr[3] {
	case socksAtypIPv4:
		var ip [4]byte
		if _, err := io.ReadFull(conn, ip[:]); err != nil {
			return "", err
		}
		host = net.IP(ip[:]).String()
	case socksAtypDomain:
		var lenBuf [1]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return "", err
		}
		dom := make([]byte, int(lenBuf[0]))
		if _, err := io.ReadFull(conn, dom); err != nil {
			return "", err
		}
		host = string(dom)
	case socksAtypIPv6:
		var ip [16]byte
		if _, err := io.ReadFull(conn, ip[:]); err != nil {
			return "", err
		}
		host = net.IP(ip[:]).String()
	default:
		_ = writeSOCKS5Reply(conn, socksReplyAddrNotSupported)
		return "", fmt.Errorf("socks5 unsupported atyp %d", hdr[3])
	}

	var portBuf [2]byte
	if _, err := io.ReadFull(conn, portBuf[:]); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf[:])
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func writeSOCKS5Reply(conn net.Conn, rep byte) error {
	reply := []byte{
		socksVersion5,
		rep,
		0x00,          // RSV
		socksAtypIPv4, // ATYP
		0, 0, 0, 0,    // BND.ADDR
		0, 0, // BND.PORT
	}
	_, err := conn.Write(reply)
	return err
}

// RelayBidirectional copies traffic in both directions between a and b and blocks
// until both copy goroutines exit, ensuring zero leaked goroutines.
func RelayBidirectional(ctx context.Context, a, b net.Conn) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = a.Close()
			_ = b.Close()
		case <-done:
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	copySide := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		// Close write side or whole conn so the peer io.Copy unblocks cleanly.
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go copySide(b, a)
	go copySide(a, b)
	wg.Wait()
	close(done)
	_ = a.Close()
	_ = b.Close()
}
