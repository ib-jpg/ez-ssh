package ssh

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
)

// TunnelState represents the runtime lifecycle of a port forward.
type TunnelState int

const (
	TunnelStopped TunnelState = iota
	TunnelStarting
	TunnelActive
	TunnelError
)

// TunnelStatus describes the live status of a port forward.
type TunnelStatus struct {
	ID          int64
	State       TunnelState
	ActiveConns int64
	Error       string
	StartedAt   time.Time
}

// TunnelEvent is emitted whenever a tunnel changes state or connection count.
type TunnelEvent struct {
	Status TunnelStatus
}

type managedTunnel struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// TunnelManager manages background SSH port forwarding goroutines with strict
// context cancellation and graceful shutdown.
type TunnelManager struct {
	mu             sync.RWMutex
	entries        map[int64]*managedTunnel
	statuses       map[int64]TunnelStatus
	events         chan TunnelEvent
	rootCtx        context.Context
	rootCancel     context.CancelFunc
	wg             sync.WaitGroup
	vault          *crypto.Vault
	knownHostsPath string
}

// NewTunnelManager creates a lifecycle manager for background SSH tunnels.
func NewTunnelManager(vault *crypto.Vault, knownHostsPath string) *TunnelManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &TunnelManager{
		entries:        make(map[int64]*managedTunnel),
		statuses:       make(map[int64]TunnelStatus),
		events:         make(chan TunnelEvent, 64),
		rootCtx:        ctx,
		rootCancel:     cancel,
		vault:          vault,
		knownHostsPath: knownHostsPath,
	}
}

// Events returns a read-only channel of tunnel state updates.
func (m *TunnelManager) Events() <-chan TunnelEvent {
	return m.events
}

// Status returns the current runtime status for tunnel id.
func (m *TunnelManager) Status(id int64) TunnelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if st, ok := m.statuses[id]; ok {
		return st
	}
	return TunnelStatus{ID: id, State: TunnelStopped}
}

// ActiveCount returns the number of currently starting or active tunnels.
func (m *TunnelManager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, st := range m.statuses {
		if st.State == TunnelActive || st.State == TunnelStarting {
			n++
		}
	}
	return n
}

// IsRunning reports whether tunnel id is currently starting or active.
func (m *TunnelManager) IsRunning(id int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.entries[id]
	return ok
}

// Toggle starts a stopped/errored tunnel or stops a running tunnel.
func (m *TunnelManager) Toggle(pf db.PortForward, host db.Host, kc *db.KeychainItem) error {
	if m.IsRunning(pf.ID) {
		m.Stop(pf.ID)
		return nil
	}
	return m.Start(pf, host, kc)
}

// Start launches tunnel pf in a managed background goroutine.
func (m *TunnelManager) Start(pf db.PortForward, host db.Host, kc *db.KeychainItem) error {
	m.mu.Lock()
	if m.rootCtx.Err() != nil {
		m.mu.Unlock()
		return context.Canceled
	}
	if _, exists := m.entries[pf.ID]; exists {
		m.mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(m.rootCtx)
	done := make(chan struct{})
	m.entries[pf.ID] = &managedTunnel{cancel: cancel, done: done}
	st := TunnelStatus{ID: pf.ID, State: TunnelStarting}
	m.statuses[pf.ID] = st
	m.mu.Unlock()

	m.emit(st)
	m.wg.Add(1)
	go m.runTunnel(ctx, pf, host, kc, done)
	return nil
}

// Stop cancels a running tunnel and waits for its goroutine to tear down.
func (m *TunnelManager) Stop(id int64) {
	m.mu.RLock()
	mt, ok := m.entries[id]
	m.mu.RUnlock()
	if !ok {
		return
	}
	mt.cancel()
	<-mt.done
}

// ShutdownAll cancels all running tunnels and waits for every goroutine to exit.
func (m *TunnelManager) ShutdownAll() {
	m.rootCancel()
	m.wg.Wait()
}

func (m *TunnelManager) runTunnel(
	ctx context.Context,
	pf db.PortForward,
	host db.Host,
	kc *db.KeychainItem,
	done chan struct{},
) {
	defer m.wg.Done()
	defer close(done)

	fail := func(err error) {
		m.mu.Lock()
		delete(m.entries, pf.ID)
		st := TunnelStatus{
			ID:    pf.ID,
			State: TunnelError,
			Error: err.Error(),
		}
		m.statuses[pf.ID] = st
		m.mu.Unlock()
		m.emit(st)
	}

	stopped := func() {
		m.mu.Lock()
		delete(m.entries, pf.ID)
		st := TunnelStatus{
			ID:    pf.ID,
			State: TunnelStopped,
		}
		m.statuses[pf.ID] = st
		m.mu.Unlock()
		m.emit(st)
	}

	sshClient, err := DialHost(ctx, host, kc, m.vault, m.knownHostsPath)
	if err != nil {
		if ctx.Err() != nil {
			stopped()
			return
		}
		fail(err)
		return
	}
	defer sshClient.Close()

	localAddr := pf.LocalAddr
	if localAddr == "" {
		localAddr = "127.0.0.1"
	}
	remoteAddr := pf.RemoteAddr
	if remoteAddr == "" {
		remoteAddr = "127.0.0.1"
	}

	var ln net.Listener
	switch pf.Type {
	case db.TunnelLocal, db.TunnelDynamic:
		bind := net.JoinHostPort(localAddr, strconv.Itoa(pf.LocalPort))
		ln, err = net.Listen("tcp", bind)
		if err != nil {
			fail(fmt.Errorf("listen local %s: %w", bind, err))
			return
		}
	case db.TunnelRemote:
		bind := net.JoinHostPort(remoteAddr, strconv.Itoa(pf.RemotePort))
		ln, err = sshClient.Listen("tcp", bind)
		if err != nil {
			fail(fmt.Errorf("listen remote %s: %w", bind, err))
			return
		}
	default:
		fail(fmt.Errorf("unsupported tunnel type %q", pf.Type))
		return
	}
	defer ln.Close()

	startedAt := time.Now()
	m.updateActive(pf.ID, 0, startedAt)

	var activeConns atomic.Int64
	var connWG sync.WaitGroup
	errCh := make(chan error, 2)

	// Close listener and SSH client when context cancels so Accept() unblocks immediately.
	watcherDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
			_ = sshClient.Close()
		case <-watcherDone:
		}
	}()

	// Periodic SSH keepalive checks tunnel liveness.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-watcherDone:
				return
			case <-ticker.C:
				if _, _, kerr := sshClient.SendRequest("keepalive@openssh.com", true, nil); kerr != nil {
					if ctx.Err() == nil {
						select {
						case errCh <- fmt.Errorf("tunnel keepalive lost: %w", kerr):
						default:
						}
						_ = ln.Close()
					}
					return
				}
			}
		}
	}()

	// Accept loop
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				if ctx.Err() == nil {
					select {
					case errCh <- fmt.Errorf("accept failed: %w", aerr):
					default:
					}
				} else {
					select {
					case errCh <- nil:
					default:
					}
				}
				return
			}

			cur := activeConns.Add(1)
			m.updateActive(pf.ID, cur, startedAt)

			connWG.Add(1)
			go func(accepted net.Conn) {
				defer connWG.Done()
				defer func() {
					rem := activeConns.Add(-1)
					if ctx.Err() == nil {
						m.updateActive(pf.ID, rem, startedAt)
					}
				}()

				switch pf.Type {
				case db.TunnelLocal:
					target := net.JoinHostPort(remoteAddr, strconv.Itoa(pf.RemotePort))
					remoteConn, derr := sshClient.DialContext(ctx, "tcp", target)
					if derr != nil {
						_ = accepted.Close()
						return
					}
					RelayBidirectional(ctx, accepted, remoteConn)

				case db.TunnelRemote:
					target := net.JoinHostPort(localAddr, strconv.Itoa(pf.LocalPort))
					localConn, derr := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", target)
					if derr != nil {
						_ = accepted.Close()
						return
					}
					RelayBidirectional(ctx, accepted, localConn)

				case db.TunnelDynamic:
					_ = ServeSOCKS5(ctx, accepted, sshClient.DialContext)
				}
			}(c)
		}
	}()

	runErr := <-errCh
	_ = ln.Close()
	_ = sshClient.Close()
	close(watcherDone)
	connWG.Wait()

	if runErr != nil && ctx.Err() == nil {
		fail(runErr)
		return
	}
	stopped()
}

func (m *TunnelManager) updateActive(id, activeConns int64, startedAt time.Time) {
	m.mu.Lock()
	if _, running := m.entries[id]; !running {
		m.mu.Unlock()
		return
	}
	st := TunnelStatus{
		ID:          id,
		State:       TunnelActive,
		ActiveConns: activeConns,
		StartedAt:   startedAt,
	}
	m.statuses[id] = st
	m.mu.Unlock()
	m.emit(st)
}

func (m *TunnelManager) emit(st TunnelStatus) {
	ev := TunnelEvent{Status: st}
	select {
	case m.events <- ev:
	default:
		// Drop oldest unread event to preserve latest state without blocking.
		select {
		case <-m.events:
		default:
		}
		select {
		case m.events <- ev:
		default:
		}
	}
}
