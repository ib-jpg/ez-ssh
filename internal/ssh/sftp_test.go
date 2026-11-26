package ssh

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
)

func TestSFTPClientOperationsAndTransfers(t *testing.T) {
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, "local")
	remoteDir := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatal(err)
	}

	vault, err := crypto.NewVault(filepath.Join(tmp, ".vault.key"))
	if err != nil {
		t.Fatal(err)
	}
	khPath := filepath.Join(tmp, "known_hosts")

	srv := startTestSSHServer(t, nil)
	srv.sftpRoot = remoteDir

	pwEnc, _ := vault.Encrypt("secret-pw")
	host := db.Host{
		ID: 1, Name: "sftp-box", Hostname: srv.host, Port: srv.port, Username: "testuser",
		InlineSecretEnc: pwEnc,
	}

	cli, err := ConnectSFTP(context.Background(), host, nil, vault, khPath)
	if err != nil {
		t.Fatalf("ConnectSFTP error = %v", err)
	}
	defer cli.Close()

	// Create local test file + subfolder
	payload := bytes.Repeat([]byte("ez-ssh-sftp-payload-0123456789\n"), 2000) // ~66 KB
	srcFile := filepath.Join(localDir, "upload.bin")
	if err := os.WriteFile(srcFile, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	// Upload file
	var gotProgress bool
	if err := cli.Upload(context.Background(), srcFile, remoteDir, func(p TransferProgress) {
		if p.BytesDone > 0 && p.TotalBytes == int64(len(payload)) {
			gotProgress = true
		}
	}); err != nil {
		t.Fatalf("Upload error = %v", err)
	}
	if !gotProgress {
		t.Fatal("expected upload progress callback")
	}

	// Mkdir remote & rename
	if err := cli.MkdirRemote(remoteDir, "sub"); err != nil {
		t.Fatalf("MkdirRemote error = %v", err)
	}
	if err := cli.RenameRemote(filepath.Join(remoteDir, "upload.bin"), "renamed.bin"); err != nil {
		t.Fatalf("RenameRemote error = %v", err)
	}

	// ListRemote
	_, entries, err := cli.ListRemote(remoteDir, false)
	if err != nil {
		t.Fatalf("ListRemote error = %v", err)
	}
	foundSub, foundFile := false, false
	for _, e := range entries {
		if e.Name == "sub" && e.IsDir {
			foundSub = true
		}
		if e.Name == "renamed.bin" && !e.IsDir && e.Size == int64(len(payload)) {
			foundFile = true
		}
	}
	if !foundSub || !foundFile {
		t.Fatalf("unexpected remote entries = %+v", entries)
	}

	// Download and verify byte equality
	dlDir := filepath.Join(tmp, "dl")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cli.Download(context.Background(), filepath.Join(remoteDir, "renamed.bin"), dlDir, nil); err != nil {
		t.Fatalf("Download error = %v", err)
	}
	gotBytes, err := os.ReadFile(filepath.Join(dlDir, "renamed.bin"))
	if err != nil || !bytes.Equal(gotBytes, payload) {
		t.Fatalf("downloaded bytes mismatch: err=%v len=%d want=%d", err, len(gotBytes), len(payload))
	}

	// Recursive remote remove
	if err := cli.RemoveRemote(filepath.Join(remoteDir, "sub")); err != nil {
		t.Fatalf("RemoveRemote(dir) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(remoteDir, "sub")); !os.IsNotExist(err) {
		t.Fatalf("expected remote dir deleted, stat err = %v", err)
	}
}
