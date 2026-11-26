package views

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ib-jpg/ez-ssh/internal/ssh"
)

func newTestSFTPView(t *testing.T, w, h int) *SFTPView {
	t.Helper()
	v := &SFTPView{localDir: "/tmp"}
	v.SetSize(w, h)
	return v
}

func toastText(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a toast command")
	}
	msg, ok := cmd().(ToastMsg)
	if !ok {
		t.Fatalf("expected ToastMsg, got %T", cmd())
	}
	return msg.Text
}

func TestSFTPTransferButtons(t *testing.T) {
	v := newTestSFTPView(t, 90, 24)
	v.localEntries = []ssh.FileEntry{
		{Name: "..", FullPath: "/", IsDir: true, IsParent: true},
		{Name: "notes.txt", FullPath: "/tmp/notes.txt"},
	}
	v.localCursor = 1

	view := v.View(90, 24)
	if !strings.Contains(view, "Upload") || !strings.Contains(view, "Download") {
		t.Fatalf("transfer row not rendered:\n%s", view)
	}
	if !v.transferRowVisible {
		t.Fatal("expected the transfer row to be visible")
	}
	if !v.uploadBtnRect.contains(v.uploadBtnRect.x0+1, 1) || !v.downloadBtnRect.contains(v.downloadBtnRect.x0+1, 1) {
		t.Fatalf("invalid button hitboxes: up=%+v down=%+v", v.uploadBtnRect, v.downloadBtnRect)
	}

	// No host connected yet: both buttons should still respond with guidance.
	up := v.handleMouse(tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
		X: v.uploadBtnRect.x0 + 1, Y: 1,
	})
	if text := toastText(t, up); !strings.Contains(text, "Connect to a remote host") {
		t.Fatalf("unexpected upload toast: %q", text)
	}

	down := v.handleMouse(tea.MouseMsg{
		Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
		X: v.downloadBtnRect.x0 + 1, Y: 1,
	})
	if text := toastText(t, down); !strings.Contains(text, "Connect to a remote host") {
		t.Fatalf("unexpected download toast: %q", text)
	}
}

func TestSFTPDragDropLocalToRemote(t *testing.T) {
	v := newTestSFTPView(t, 90, 24)
	v.localEntries = []ssh.FileEntry{
		{Name: "..", FullPath: "/", IsDir: true, IsParent: true},
		{Name: "notes.txt", FullPath: "/tmp/notes.txt"},
	}
	_ = v.View(90, 24)

	rowY := v.paneTop() + 4 + 1 // border + title + col header + separator + row 1
	v.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 3, Y: rowY})
	if !v.dragCandidate || v.dragPane != paneLocal || v.dragEntry.Name != "notes.txt" {
		t.Fatalf("drag was not armed: candidate=%v pane=%d entry=%+v", v.dragCandidate, v.dragPane, v.dragEntry)
	}

	v.handleMouse(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 60, Y: rowY + 1})
	if !v.dragging {
		t.Fatal("expected dragging to start after motion")
	}
	if view := v.View(90, 24); !strings.Contains(view, "release in remote pane") {
		t.Fatalf("drag hint not rendered:\n%s", view)
	}

	cmd := v.handleMouse(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 60, Y: rowY})
	if text := toastText(t, cmd); !strings.Contains(text, "Connect to a remote host") {
		t.Fatalf("drop did not trigger an upload: %q", text)
	}
	if v.dragCandidate || v.dragging {
		t.Fatal("drag state was not cleared after drop")
	}
	if v.localCursor != 1 {
		t.Fatalf("cursor should point at the dragged entry, got %d", v.localCursor)
	}
}

func TestSFTPDragDropRemoteToLocal(t *testing.T) {
	v := newTestSFTPView(t, 90, 24)
	v.remoteEntries = []ssh.FileEntry{
		{Name: "..", FullPath: "/", IsDir: true, IsParent: true},
		{Name: "remote.log", FullPath: "/home/u/remote.log"},
	}
	_ = v.View(90, 24)

	rowY := v.paneTop() + 4 + 1
	v.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 60, Y: rowY})
	v.handleMouse(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 3, Y: rowY})

	cmd := v.handleMouse(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 3, Y: rowY})
	if text := toastText(t, cmd); !strings.Contains(text, "Connect to a remote host") {
		t.Fatalf("drop did not trigger a download: %q", text)
	}
	if v.remoteCursor != 1 {
		t.Fatalf("cursor should point at the dragged entry, got %d", v.remoteCursor)
	}
}

func TestSFTPPlainClickIsNotADrag(t *testing.T) {
	v := newTestSFTPView(t, 90, 24)
	v.localEntries = []ssh.FileEntry{
		{Name: "..", FullPath: "/", IsDir: true, IsParent: true},
		{Name: "notes.txt", FullPath: "/tmp/notes.txt"},
	}
	_ = v.View(90, 24)

	rowY := v.paneTop() + 4 + 1
	v.handleMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 3, Y: rowY})
	cmd := v.handleMouse(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 3, Y: rowY})
	if cmd != nil {
		t.Fatalf("plain click should not transfer: %v", cmd())
	}
	if v.dragCandidate || v.dragging {
		t.Fatal("drag state should be cleared after a plain click")
	}
}
