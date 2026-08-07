package upload

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/vfstypes"
)

type PendingUpload = vfstypes.PendingUpload
type UploadReplacement = vfstypes.UploadReplacement

func UploadReplacementID(u *UploadReplacement) string {
	if u == nil {
		return ""
	}
	return u.ID
}

func UploadReplacementFromEntry(entry drive.Entry) UploadReplacement {
	return UploadReplacement{ID: entry.ID, ParentID: entry.ParentID, Name: entry.Name, Size: entry.Size}
}

func UploadReplacementToEntry(u UploadReplacement) drive.Entry {
	return drive.Entry{ID: u.ID, ParentID: u.ParentID, Name: u.Name, Size: u.Size, IsDir: false}
}

type Snapshot struct {
	Path        string
	Hashes      drive.SourceHashes
	Incremental bool
}

type RemoteOps interface {
	Stat(ctx context.Context, id string) (drive.Entry, error)
	List(ctx context.Context, parentID string) ([]drive.Entry, error)
	PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error)
	Remove(ctx context.Context, entry drive.Entry) error
	Rename(ctx context.Context, entry drive.Entry, newName string) error
	CanWrite() bool
}

type Observer interface {
	Start(p PendingUpload)
	Event(path, phase string, started time.Time, bytes int64, extra map[string]any)
	Extra(path, key string, value any)
	State(path, state string)
	Finish(path, state, lastErr string)
	Metadata(path, remoteID string, hashes []string)
	HealthResult(op string, err error)
}

type Store interface {
	UploadByPath(path string) (PendingUpload, bool)
	RecordReplacementIfUnchanged(p PendingUpload, repl UploadReplacement) (PendingUpload, bool, error)
	RecordFailureIfUnchanged(p PendingUpload, err error, retryDelay time.Duration) (PendingUpload, bool, error)
	RecordPermanentFailureIfUnchanged(p PendingUpload, err error) (PendingUpload, bool, error)
	RemoveIfUnchanged(p PendingUpload) (bool, error)
	RemoveStaging(localPath string) error
	RemoveStagingIfUnreferenced(localPath string)
}

type Runtime interface {
	ClearUploadHashes(fid string)
	ModTimeFor(path string) time.Time
	RetryDelay(retryCount int) time.Duration
	Requeue(p PendingUpload)
	RequeueIfFrozen(p PendingUpload)
	SeedReadCache(entry drive.Entry, localPath string)
	CommitUploadedEntry(path string, entry drive.Entry)
}

type Snapshotter interface {
	SnapshotPending(p PendingUpload) (Snapshot, error)
}

type FaultController interface {
	ApplyCancelFault(ctx context.Context, p PendingUpload, progress drive.UploadProgress, obs Observer) (uploadCtx context.Context, uploadProgress drive.UploadProgress, cleanup func())
}
