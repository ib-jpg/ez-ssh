package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/ib-jpg/ez-ssh/internal/crypto"
	"github.com/ib-jpg/ez-ssh/internal/db"
)

// FileEntry represents a local or remote filesystem entry for SFTP browsing.
type FileEntry struct {
	Name     string
	FullPath string
	IsDir    bool
	IsParent bool
	Size     int64
	Mode     os.FileMode
	ModTime  time.Time
}

// TransferProgress reports live file transfer metrics.
type TransferProgress struct {
	Filename    string
	BytesDone   int64
	TotalBytes  int64
	BytesPerSec float64
}

// SFTPClient wraps a connected SSH + SFTP session for dual-pane file management.
type SFTPClient struct {
	sshClient *ssh.Client
	client    *sftp.Client
	closeOnce sync.Once
}

// ConnectSFTP dials host over SSH and opens an SFTP subsystem client.
func ConnectSFTP(
	ctx context.Context,
	host db.Host,
	kc *db.KeychainItem,
	vault *crypto.Vault,
	knownHostsPath string,
) (*SFTPClient, error) {
	sshCli, err := DialHost(ctx, host, kc, vault, knownHostsPath)
	if err != nil {
		return nil, err
	}
	sc, err := sftp.NewClient(sshCli)
	if err != nil {
		_ = sshCli.Close()
		return nil, fmt.Errorf("open sftp subsystem: %w", err)
	}
	return &SFTPClient{sshClient: sshCli, client: sc}, nil
}

// HomeDir returns the remote user's default working directory.
func (c *SFTPClient) HomeDir() (string, error) {
	if c == nil || c.client == nil {
		return "/", errors.New("sftp not connected")
	}
	wd, err := c.client.Getwd()
	if err != nil || wd == "" {
		return "/", nil
	}
	return wd, nil
}

// ListRemote lists entries in dir on the remote host (directories first, sorted).
func (c *SFTPClient) ListRemote(dir string, showHidden bool) (string, []FileEntry, error) {
	if c == nil || c.client == nil {
		return "", nil, errors.New("sftp not connected")
	}
	clean := path.Clean(strings.TrimSpace(dir))
	if clean == "." || clean == "" {
		var err error
		clean, err = c.HomeDir()
		if err != nil {
			return "", nil, err
		}
	}

	infos, err := c.client.ReadDir(clean)
	if err != nil {
		return clean, nil, fmt.Errorf("list remote %s: %w", clean, err)
	}

	entries := make([]FileEntry, 0, len(infos)+1)
	if clean != "/" {
		entries = append(entries, FileEntry{
			Name:     "..",
			FullPath: path.Dir(clean),
			IsDir:    true,
			IsParent: true,
		})
	}

	var items []FileEntry
	for _, info := range infos {
		name := info.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		isDir := info.IsDir()
		if info.Mode()&os.ModeSymlink != 0 {
			if targetInfo, stErr := c.client.Stat(path.Join(clean, name)); stErr == nil {
				isDir = targetInfo.IsDir()
			}
		}
		items = append(items, FileEntry{
			Name:     name,
			FullPath: path.Join(clean, name),
			IsDir:    isDir,
			Size:     info.Size(),
			Mode:     info.Mode(),
			ModTime:  info.ModTime(),
		})
	}
	sortFileEntries(items)
	return clean, append(entries, items...), nil
}

// ListLocal lists entries in a local directory (directories first, sorted).
func ListLocal(dir string, showHidden bool) (string, []FileEntry, error) {
	if strings.TrimSpace(dir) == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		} else {
			dir = "."
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir, nil, err
	}

	dirEntries, err := os.ReadDir(abs)
	if err != nil {
		return abs, nil, fmt.Errorf("list local %s: %w", abs, err)
	}

	entries := make([]FileEntry, 0, len(dirEntries)+1)
	parent := filepath.Dir(abs)
	if parent != abs {
		entries = append(entries, FileEntry{
			Name:     "..",
			FullPath: parent,
			IsDir:    true,
			IsParent: true,
		})
	}

	var items []FileEntry
	for _, de := range dirEntries {
		name := de.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		info, ierr := de.Info()
		if ierr != nil {
			continue
		}
		isDir := de.IsDir()
		if info.Mode()&os.ModeSymlink != 0 {
			if targetInfo, stErr := os.Stat(filepath.Join(abs, name)); stErr == nil {
				isDir = targetInfo.IsDir()
			}
		}
		items = append(items, FileEntry{
			Name:     name,
			FullPath: filepath.Join(abs, name),
			IsDir:    isDir,
			Size:     info.Size(),
			Mode:     info.Mode(),
			ModTime:  info.ModTime(),
		})
	}
	sortFileEntries(items)
	return abs, append(entries, items...), nil
}

func sortFileEntries(items []FileEntry) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
}

// Upload copies localPath (file or directory) into remoteDir with live throttled progress.
func (c *SFTPClient) Upload(ctx context.Context, localPath, remoteDir string, onProgress func(TransferProgress)) error {
	if c == nil || c.client == nil {
		return errors.New("sftp not connected")
	}

	totalBytes, err := calcLocalSize(localPath)
	if err != nil {
		return err
	}

	start := time.Now()
	var doneBytes int64
	lastReport := time.Time{}

	emit := func(name string, force bool) {
		if onProgress == nil {
			return
		}
		now := time.Now()
		if !force && !lastReport.IsZero() && now.Sub(lastReport) < 80*time.Millisecond {
			return
		}
		lastReport = now
		elapsed := now.Sub(start).Seconds()
		speed := 0.0
		if elapsed > 0 {
			speed = float64(doneBytes) / elapsed
		}
		onProgress(TransferProgress{
			Filename:    name,
			BytesDone:   doneBytes,
			TotalBytes:  totalBytes,
			BytesPerSec: speed,
		})
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}

	baseName := filepath.Base(localPath)
	if !info.IsDir() {
		return c.uploadFile(ctx, localPath, path.Join(remoteDir, baseName), baseName, &doneBytes, emit)
	}

	targetRoot := path.Join(remoteDir, baseName)
	return filepath.WalkDir(localPath, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, err := filepath.Rel(localPath, p)
		if err != nil {
			return err
		}
		remoteTarget := targetRoot
		if rel != "." {
			remoteTarget = path.Join(targetRoot, filepath.ToSlash(rel))
		}
		if d.IsDir() {
			return c.client.MkdirAll(remoteTarget)
		}
		return c.uploadFile(ctx, p, remoteTarget, d.Name(), &doneBytes, emit)
	})
}

func (c *SFTPClient) uploadFile(
	ctx context.Context,
	localFile, remoteFile, displayName string,
	doneBytes *int64,
	emit func(string, bool),
) error {
	src, err := os.Open(localFile)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := c.client.OpenFile(remoteFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("create remote %s: %w", remoteFile, err)
	}
	defer dst.Close()

	if err := copyWithProgress(ctx, dst, src, func(n int64) {
		*doneBytes += n
		emit(displayName, false)
	}); err != nil {
		return err
	}
	emit(displayName, true)
	return nil
}

// Download copies remotePath (file or directory) into localDir with live throttled progress.
func (c *SFTPClient) Download(ctx context.Context, remotePath, localDir string, onProgress func(TransferProgress)) error {
	if c == nil || c.client == nil {
		return errors.New("sftp not connected")
	}

	totalBytes, err := c.calcRemoteSize(remotePath)
	if err != nil {
		return err
	}

	start := time.Now()
	var doneBytes int64
	lastReport := time.Time{}

	emit := func(name string, force bool) {
		if onProgress == nil {
			return
		}
		now := time.Now()
		if !force && !lastReport.IsZero() && now.Sub(lastReport) < 80*time.Millisecond {
			return
		}
		lastReport = now
		elapsed := now.Sub(start).Seconds()
		speed := 0.0
		if elapsed > 0 {
			speed = float64(doneBytes) / elapsed
		}
		onProgress(TransferProgress{
			Filename:    name,
			BytesDone:   doneBytes,
			TotalBytes:  totalBytes,
			BytesPerSec: speed,
		})
	}

	info, err := c.client.Stat(remotePath)
	if err != nil {
		return err
	}
	baseName := path.Base(remotePath)
	if !info.IsDir() {
		return c.downloadFile(ctx, remotePath, filepath.Join(localDir, baseName), baseName, &doneBytes, emit)
	}

	return c.downloadDir(ctx, remotePath, filepath.Join(localDir, baseName), &doneBytes, emit)
}

func (c *SFTPClient) downloadDir(
	ctx context.Context,
	remoteDir, localDir string,
	doneBytes *int64,
	emit func(string, bool),
) error {
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return err
	}
	infos, err := c.client.ReadDir(remoteDir)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rp := path.Join(remoteDir, info.Name())
		lp := filepath.Join(localDir, info.Name())
		if info.IsDir() {
			if err := c.downloadDir(ctx, rp, lp, doneBytes, emit); err != nil {
				return err
			}
		} else {
			if err := c.downloadFile(ctx, rp, lp, info.Name(), doneBytes, emit); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *SFTPClient) downloadFile(
	ctx context.Context,
	remoteFile, localFile, displayName string,
	doneBytes *int64,
	emit func(string, bool),
) error {
	src, err := c.client.Open(remoteFile)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(localFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create local %s: %w", localFile, err)
	}

	copyErr := copyWithProgress(ctx, dst, src, func(n int64) {
		*doneBytes += n
		emit(displayName, false)
	})
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(localFile)
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	emit(displayName, true)
	return nil
}

func copyWithProgress(ctx context.Context, dst io.Writer, src io.Reader, onChunk func(int64)) error {
	buf := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			if nw > 0 && onChunk != nil {
				onChunk(int64(nw))
			}
			if ew != nil {
				return ew
			}
			if nw != nr {
				return io.ErrShortWrite
			}
		}
		if er != nil {
			if errors.Is(er, io.EOF) {
				return nil
			}
			return er
		}
	}
}

func calcLocalSize(p string) (int64, error) {
	info, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	err = filepath.WalkDir(p, func(_ string, d fs.DirEntry, we error) error {
		if we != nil {
			return we
		}
		if !d.IsDir() {
			if fi, fiErr := d.Info(); fiErr == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total, err
}

func (c *SFTPClient) calcRemoteSize(p string) (int64, error) {
	info, err := c.client.Stat(p)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	infos, err := c.client.ReadDir(p)
	if err != nil {
		return 0, err
	}
	for _, item := range infos {
		sz, err := c.calcRemoteSize(path.Join(p, item.Name()))
		if err != nil {
			return 0, err
		}
		total += sz
	}
	return total, nil
}

func (c *SFTPClient) MkdirRemote(parentDir, name string) error {
	if c == nil || c.client == nil {
		return errors.New("sftp not connected")
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "/") {
		return errors.New("invalid folder name")
	}
	return c.client.Mkdir(path.Join(parentDir, name))
}

func MkdirLocal(parentDir, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\`) {
		return errors.New("invalid folder name")
	}
	return os.Mkdir(filepath.Join(parentDir, name), 0o755)
}

func (c *SFTPClient) RenameRemote(oldPath, newName string) error {
	if c == nil || c.client == nil {
		return errors.New("sftp not connected")
	}
	newName = strings.TrimSpace(newName)
	if newName == "" || strings.Contains(newName, "/") {
		return errors.New("invalid name")
	}
	return c.client.Rename(oldPath, path.Join(path.Dir(oldPath), newName))
}

func RenameLocal(oldPath, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" || strings.ContainsAny(newName, `/\`) {
		return errors.New("invalid name")
	}
	return os.Rename(oldPath, filepath.Join(filepath.Dir(oldPath), newName))
}

func (c *SFTPClient) RemoveRemote(target string) error {
	if c == nil || c.client == nil {
		return errors.New("sftp not connected")
	}
	info, err := c.client.Lstat(target)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return c.client.Remove(target)
	}
	children, err := c.client.ReadDir(target)
	if err != nil {
		return err
	}
	for _, ch := range children {
		if err := c.RemoveRemote(path.Join(target, ch.Name())); err != nil {
			return err
		}
	}
	return c.client.RemoveDirectory(target)
}

func RemoveLocal(target string) error {
	return os.RemoveAll(target)
}

// Close terminates both the underlying SSH transport and the SFTP client.
// Closing sshClient first ensures any blocked SFTP packet read unblocks immediately.
func (c *SFTPClient) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		if c.sshClient != nil {
			err = c.sshClient.Close()
		}
		if c.client != nil {
			if cerr := c.client.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}
	})
	return err
}

// HumanBytes formats a byte size concisely.
func HumanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
