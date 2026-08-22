package upload

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
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
	Uploaded(path string, n int)
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
}

// UploadView is the view surface a completed upload commits to: it seeds
// the read cache from the staging file (when one exists) and folds the
// uploaded entry into the effective view. Separate from Runtime so the
// view commit is an explicit narrow dependency.
type UploadView interface {
	CommitUploadedEntry(path string, entry drive.Entry, stagingPath string)
}

// InvalidationSink publishes a path after its pending upload has been removed
// and the committed entry is the only identity visible to readers.
type InvalidationSink interface {
	InvalidatePath(path string)
}

type Snapshotter interface {
	SnapshotPending(p PendingUpload) (Snapshot, error)
}

type FaultController interface {
	ApplyCancelFault(ctx context.Context, p PendingUpload, progress drive.UploadProgress, obs Observer) (uploadCtx context.Context, uploadProgress drive.UploadProgress, cleanup func())
}
