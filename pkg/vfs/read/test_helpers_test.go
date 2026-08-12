package read

import (
	"context"
	"io"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// stubHost is a no-op Host for unit tests of read-domain logic. It never
// implements ReadObserver, so tests pair it with an explicit observer to
// prove observer injection is constructive.
type stubHost struct {
	resolveErr error
}

func (h stubHost) Resolve(context.Context, string) (drive.Entry, error) {
	if h.resolveErr != nil {
		return drive.Entry{}, h.resolveErr
	}
	return drive.Entry{}, nil
}

func (stubHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{}, false, nil
}

func (stubHost) FlushStaging(string) error { return nil }

func (stubHost) ReadCacheKey(drive.Entry) string { return "" }

func (stubHost) DriverRead(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, nil
}
