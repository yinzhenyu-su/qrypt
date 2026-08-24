// Package delete defines the interfaces for the delete subsystem.
// The concrete DeleteService implementation lives in the parent vfs package.
package delete

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Service is the delete subsystem entry point. VFS's DeleteService
// satisfies this interface.
type Service interface {
	Close()
}

// ExecutorDeps collects the interfaces the delete executor needs.
type ExecutorDeps struct {
	Driver                   DriverOps
	Overlay                  OverlayOps
	Health                   HealthRecorder
	Upload                   CleanupOps
	WaitForDescendantDeletes func(context.Context, string) error
}

// CleanupOps removes VFS upload state for a remotely deleted path.
type CleanupOps interface {
	RemoveUploadState(path string)
}

// DriverOps is the driver subset used during remote delete.
type DriverOps interface {
	Remove(ctx context.Context, entry drive.Entry) error
}

// OverlayOps manages the view overlay for deleted-file tracking.
type OverlayOps interface {
	BeginDelete(path, entryID string) bool
	MarkDeleteActive(path string, entry drive.Entry)
	MarkDeleteFailed(path string, err error)
	MarkDeleteComplete(path string, entry drive.Entry)
	CancelDelete(path string)
}

// HealthRecorder records health metrics for delete operations.
type HealthRecorder interface {
	RecordResult(op string, err error)
}
