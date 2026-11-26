package clipboard

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestClipboardWriteReadRoundTrip(t *testing.T) {
	const payload = "ez-ssh clipboard round trip"
	via, err := Write(payload)
	if err != nil {
		t.Skipf("no clipboard backend available: %v", err)
	}
	got, err := Read()
	if err != nil {
		t.Fatalf("Read error = %v (write via %s)", err, via)
	}
	if got != payload {
		t.Fatalf("clipboard = %q, want %q (write via %s)", got, payload, via)
	}
}

func TestBuildAndWriteOSC52(t *testing.T) {
	payload := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA test@lazy"
	enc := base64.StdEncoding.EncodeToString([]byte(payload))

	var buf bytes.Buffer
	if err := writeOSC52(&buf, payload, false); err != nil {
		t.Fatalf("writeOSC52 error = %v", err)
	}
	want := "\x1b]52;c;" + enc + "\a"
	if buf.String() != want {
		t.Fatalf("OSC52 = %q, want %q", buf.String(), want)
	}

	buf.Reset()
	if err := writeOSC52(&buf, payload, true); err != nil {
		t.Fatalf("writeOSC52(tmux) error = %v", err)
	}
	if !strings.HasPrefix(buf.String(), "\x1bPtmux;\x1b\x1b]52;c;") || !strings.HasSuffix(buf.String(), "\a\x1b\\") {
		t.Fatalf("unexpected tmux OSC52 sequence: %q", buf.String())
	}
}
