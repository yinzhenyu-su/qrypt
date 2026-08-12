package sync

import (
	"context"
	"io"
	"os"

	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// PutFile uploads a local file into the VFS (create, stream in chunks, flush).
func PutFile(ctx context.Context, fs vfs.Writer, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := fs.Create(ctx, remotePath); err != nil {
		return err
	}
	buf := make([]byte, 256*1024)
	var off int64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			written, err := fs.WriteAt(ctx, remotePath, buf[:n], off)
			if err != nil {
				return err
			}
			off += int64(written)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return fs.Flush(ctx, remotePath)
}

// GetFile downloads a VFS file to a local path with an atomic replace. It
// prefers the streaming surface (bounded memory) and falls back to Read for
// filesystems that only buffer whole files.
func GetFile(ctx context.Context, fs vfs.Reader, remotePath, localPath string) error {
	return util.WriteAtomic(localPath, ".qrypt-sync-*", 0o644, true, func(out *os.File) error {
		var rc io.ReadCloser
		var err error
		if streamer, ok := fs.(vfs.StreamReader); ok {
			rc, err = streamer.ReadStream(ctx, remotePath)
		} else {
			rc, err = fs.Read(ctx, remotePath, 0, 0)
		}
		if err != nil {
			return err
		}
		defer rc.Close()
		_, err = io.Copy(out, rc)
		return err
	})
}
