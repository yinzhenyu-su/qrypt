package sftp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p == "" || p == "/" || p == "." {
		return d.rootPath, nil
	}
	clean, err := rootRelativePath(p)
	if err != nil {
		return "", err
	}
	resolved := path.Join(d.rootPath, clean)
	if d.withinRoot(resolved) {
		return resolved, nil
	}
	return "", fmt.Errorf("sftp: path escapes root %q: %w", p, drive.ErrInvalidInput)
}

func (d *Driver) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	return drive.RemoteNameInfo{PlainName: plainName, RemoteName: plainName}, nil
}

func (d *Driver) Space(ctx context.Context) (space drive.Space, err error) {
	started := time.Now()
	defer func() { d.recordOperation(ctx, "space", d.rootPath, started, 0, err) }()
	client, err := d.getClient(ctx)
	if err != nil {
		return drive.Space{}, err
	}
	stat, err := client.StatVFS(d.rootPath)
	if err != nil {
		if sftpOpUnsupported(err) {
			// The server does not implement statvfs (e.g. the in-process
			// test server on Windows); capacity is simply not reported.
			return drive.Space{}, nil
		}
		return drive.Space{}, fmt.Errorf("sftp: statvfs %q: %w", d.rootPath, err)
	}
	return drive.Space{Total: statVFSBytes(stat.Blocks, stat.Bsize), Free: statVFSBytes(stat.Bfree, stat.Bsize)}, nil
}

// sftpOpUnsupported reports whether err is an SFTP "operation not supported"
// status, which some servers return for statvfs.
func sftpOpUnsupported(err error) bool {
	if errors.Is(err, sftp.ErrSSHFxOpUnsupported) {
		return true
	}
	var status *sftp.StatusError
	if errors.As(err, &status) {
		return status.FxCode() == sftp.ErrSSHFxOpUnsupported ||
			strings.Contains(strings.ToLower(status.Error()), "not support")
	}
	return false
}

func statVFSBytes(blocks, blockSize uint64) int64 {
	if blockSize != 0 && blocks > math.MaxInt64/blockSize {
		return math.MaxInt64
	}
	return int64(blocks * blockSize)
}

func (d *Driver) resolveID(id string) (string, error) {
	if id == "" || id == "/" || id == "0" {
		return d.rootPath, nil
	}
	if strings.HasPrefix(id, "/") {
		resolved := path.Clean(id)
		if d.withinRoot(resolved) {
			return resolved, nil
		}
		return "", fmt.Errorf("sftp: path escapes root %q: %w", id, drive.ErrInvalidInput)
	}
	if filepath.IsAbs(id) {
		// A host-absolute root (e.g. "C:\data" on Windows) is the remote
		// root encoded in native form; normalize to the slash-rooted form
		// before the root-relative fallback mangles it.
		resolved := path.Clean("/" + filepath.ToSlash(id))
		if d.withinRoot(resolved) {
			return resolved, nil
		}
		return "", fmt.Errorf("sftp: path escapes root %q: %w", id, drive.ErrInvalidInput)
	}
	clean, err := rootRelativePath(id)
	if err != nil {
		return "", err
	}
	resolved := path.Join(d.rootPath, clean)
	if d.withinRoot(resolved) {
		return resolved, nil
	}
	return "", fmt.Errorf("sftp: path escapes root %q: %w", id, drive.ErrInvalidInput)
}

func (d *Driver) withinRoot(candidate string) bool {
	return candidate == d.rootPath || d.rootPath == "/" || strings.HasPrefix(candidate, d.rootPath+"/")
}

func rootRelativePath(input string) (string, error) {
	depth := 0
	for _, segment := range strings.Split(strings.TrimPrefix(input, "/"), "/") {
		switch segment {
		case "", ".":
		case "..":
			if depth == 0 {
				return "", fmt.Errorf("sftp: path escapes root %q: %w", input, drive.ErrInvalidInput)
			}
			depth--
		default:
			depth++
		}
	}
	return strings.TrimPrefix(path.Clean(input), "/"), nil
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." || path.Base(name) != name {
		return fmt.Errorf("sftp: invalid entry name %q: %w", name, drive.ErrInvalidInput)
	}
	return nil
}
