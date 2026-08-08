package read

import (
	"context"
	"io"

	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// stubHost is a no-op Host for unit tests of read-domain logic.
type stubHost struct{}

func (stubHost) Resolve(context.Context, string) (drive.Entry, error) {
	return drive.Entry{}, nil
}

func (stubHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{}, false, nil
}

func (stubHost) FlushStaging(string) error { return nil }

func (stubHost) RecordHealth(string, error) {}

func (stubHost) ReadCacheKey(drive.Entry) string { return "" }

func (stubHost) RootID() string { return "" }

func (stubHost) DriverRead(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, nil
}
