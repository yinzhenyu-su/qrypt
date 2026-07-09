package cli

import (
	"context"
	"io"
	"os"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func put(ctx context.Context, fs vfs.FileSystem, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return putReader(ctx, fs, f, remotePath)
}

func putReader(ctx context.Context, fs vfs.FileSystem, reader io.Reader, remotePath string) error {
	if err := fs.Create(ctx, remotePath); err != nil {
		return err
	}
	buf := make([]byte, 256*1024)
	var off int64
	for {
		n, readErr := reader.Read(buf)
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
	if err := fs.Flush(ctx, remotePath); err != nil {
		return err
	}
	return nil
}
