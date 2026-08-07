package read

import (
	"context"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/vfstypes"
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

func (stubHost) DebugNextOpID() string { return "" }

func (stubHost) DebugBeginActive(vfstypes.DebugActiveOp) uint64 { return 0 }

func (stubHost) DebugUpdateActive(uint64, func(*vfstypes.DebugActiveOp)) {}

func (stubHost) DebugFinishActive(uint64) {}

func (stubHost) DebugRecordRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error) {
}

func (stubHost) DebugRecordReadDetail(context.Context, string, string, string, int64, int64, int64, time.Time, map[string]any, error) {
}

func (stubHost) DebugCacheCounters() (int64, int64) { return 0, 0 }
