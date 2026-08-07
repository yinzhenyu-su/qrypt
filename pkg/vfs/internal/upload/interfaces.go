// Package upload defines the interfaces for the upload subsystem.
// The concrete implementations live in the parent vfs package;
// this package exists to document the boundary and enable future
// extraction of the upload engine and worker.
package upload

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/vfstypes"
)

// PendingUpload is an alias for the shared type.
type PendingUpload = vfstypes.PendingUpload

// UploadReplacement is an alias for the shared type.
type UploadReplacement = vfstypes.UploadReplacement

// Service is the upload subsystem entry point. VFS's UploadService
// satisfies this interface.
type Service interface {
	// Scheduling
	Enqueue(p PendingUpload)
	EnqueueAfter(p PendingUpload, delay time.Duration)
	CancelUpload(path string)
	CancelChildUploads(dir string)
	QuietDelay(p PendingUpload) time.Duration

	// Store
	PendingUploads() []PendingUpload
	PendingByID(id string) (PendingUpload, bool)
	UploadByPath(path string) (PendingUpload, bool)
	SaveUpload(p PendingUpload) error
	SaveUploadExact(p PendingUpload) error
	RemoveUpload(path string) error
	RemoveUploadsUnder(path string) error
	RenameUpload(oldPath string, p PendingUpload) error

	// Hash tracking
	HashRemovePath(path string)
	HashRemoveUnder(path string)
	HashRenamePath(oldPath, newPath string, p PendingUpload)

	// Lifecycle
	Close()
}

// EngineDeps collects the interfaces the upload engine needs.
// VFS wires these from its concrete implementations.
type EngineDeps struct {
	Remote   RemoteOps
	Observer EngineObserver
	Store    EngineStore
	Runtime  EngineRuntime
	Faults   FaultController
}

// RemoteOps is the driver subset used during upload.
type RemoteOps interface {
	Stat(ctx context.Context, id string) (drive.Entry, error)
	PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error)
	Remove(ctx context.Context, entry drive.Entry) error
	CanWrite() bool
}

// EngineObserver receives lifecycle callbacks from the engine.
type EngineObserver interface {
	Start(p PendingUpload)
	Event(path, phase string, started time.Time, bytes int64, extra map[string]any)
	Extra(path, key string, value any)
	State(path, state string)
	Finish(path, state, lastErr string)
	Metadata(path, remoteID string, hashes []string)
	HealthResult(op string, err error)
}

// EngineStore is the pending-upload store subset used by the engine.
type EngineStore interface {
	UploadByPath(path string) (PendingUpload, bool)
	RecordReplacementIfUnchanged(p PendingUpload, repl UploadReplacement) (PendingUpload, bool, error)
	RecordFailureIfUnchanged(p PendingUpload, err error, retryDelay time.Duration) (PendingUpload, bool, error)
	RecordPermanentFailureIfUnchanged(p PendingUpload, err error) (PendingUpload, bool, error)
	RemoveIfUnchanged(p PendingUpload) (bool, error)
	RemoveStaging(localPath string) error
	RemoveStagingIfUnreferenced(localPath string)
}

// EngineRuntime provides VFS-side operations during upload execution.
type EngineRuntime interface {
	ClearUploadHashes(fid string)
	ModTimeFor(path string) time.Time
	RetryDelay(retryCount int) time.Duration
	Requeue(p PendingUpload)
	RequeueIfFrozen(p PendingUpload)
	SeedReadCache(entry drive.Entry, localPath string)
	CommitUploadedEntry(path string, entry drive.Entry)
}

// FaultController checks for debug-injected upload cancellations.
type FaultController interface {
	ApplyCancelFault(ctx context.Context, p PendingUpload, progress drive.UploadProgress, observer EngineObserver) (uploadCtx context.Context, uploadProgress drive.UploadProgress, cleanup func())
}
