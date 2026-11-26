package ssh

import (
	"context"
	"fmt"
	"time"

	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
)

// ExecuteSnippet connects to host over SSH, runs command via an exec session,
// and returns combined stdout+stderr along with elapsed duration.
func ExecuteSnippet(
	ctx context.Context,
	host db.Host,
	kc *db.KeychainItem,
	vault *crypto.Vault,
	knownHostsPath string,
	command string,
) (string, time.Duration, error) {
	start := time.Now()
	client, err := DialHost(ctx, host, kc, vault, knownHostsPath)
	if err != nil {
		return "", time.Since(start), err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", time.Since(start), fmt.Errorf("create ssh session: %w", err)
	}
	defer session.Close()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
			_ = client.Close()
		case <-done:
		}
	}()

	out, runErr := session.CombinedOutput(command)
	close(done)
	return string(out), time.Since(start), runErr
}
