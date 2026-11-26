package views

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestHostsToolbarHasBreathingRoomBeforeSectionTitle(t *testing.T) {
	v := &HostsView{}
	v.SetSize(90, 24)

	lines := strings.Split(ansi.Strip(v.ViewMain(90, 24)), "\n")
	if len(lines) < 6 {
		t.Fatalf("frame too short: %d lines", len(lines))
	}
	if !strings.Contains(lines[3], "New Host") {
		t.Fatalf("expected action toolbar on row 3, got %q", lines[3])
	}
	if strings.TrimSpace(lines[4]) != "" {
		t.Fatalf("expected a blank breathing row between toolbar and heading, got %q", lines[4])
	}
	if !strings.Contains(lines[5], "Hosts (") {
		t.Fatalf("expected section heading on row 5, got %q", lines[5])
	}
}
