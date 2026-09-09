package core

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const uploadCopyChunkSize = 256 * 1024

type uploadWriter interface {
	WriteAt(ctx context.Context, data []byte, offset int64) (int, error)
	Commit(ctx context.Context) (drive.Entry, error)
	Abort(ctx context.Context) error
}

type serviceUploadWriter struct {
	service *UploadService
	path    string
}

func newServiceUploadWriter(service *UploadService, path string) serviceUploadWriter {
	return serviceUploadWriter{service: service, path: path}
}

func (w serviceUploadWriter) WriteAt(ctx context.Context, data []byte, offset int64) (int, error) {
	return w.service.WriteStream(ctx, w.path, data, offset)
}

func (w serviceUploadWriter) Commit(ctx context.Context) (drive.Entry, error) {
	return w.service.FinishStream(ctx, w.path)
}

func (w serviceUploadWriter) Abort(ctx context.Context) error {
	return w.service.CancelStream(ctx, w.path)
}

func copyUploadSource(ctx context.Context, source drive.ReadOnlyFileSource, writer uploadWriter) (_ drive.Entry, err error) {
	if source == nil {
		return drive.Entry{}, fmt.Errorf("core: upload source required")
	}
	if writer == nil {
		return drive.Entry{}, fmt.Errorf("core: upload writer required")
	}
	reader, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("core: open upload source: %w", err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("core: close upload source: %w", closeErr))
		}
	}()

	buf := make([]byte, uploadCopyChunkSize)
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return drive.Entry{}, fmt.Errorf("core: copy upload source: %w", err)
		}
		n, readErr := reader.Read(buf)
		if n > 0 {
			written, writeErr := writer.WriteAt(ctx, buf[:n], offset)
			if writeErr != nil {
				return drive.Entry{}, fmt.Errorf("core: write upload source: %w", writeErr)
			}
			if written != n {
				return drive.Entry{}, fmt.Errorf("core: short staging write: wrote %d of %d", written, n)
			}
			offset += int64(written)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return drive.Entry{}, fmt.Errorf("core: read upload source: %w", readErr)
		}
	}

	entry, err := writer.Commit(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("core: commit upload source: %w", err)
	}
	return entry, nil
}
