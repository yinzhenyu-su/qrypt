// Package listing implements the VFS directory-listing domain: list
// coalescing, deterministic paging, and directory prefetch. It is driven
// by a Host interface so it stays free of VFS internals.
package listing

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Remote is the remote-IO surface the listing domain needs: path
// resolution and backend child listing. It is separate from View so the
// listing domain consumes a synthesized directory view without owning the
// overlay's sources. VFS implements it via vfsListingRemote.
type Remote interface {
	Resolve(ctx context.Context, path string) (drive.Entry, error)
	ListChildren(ctx context.Context, parentID string) ([]drive.Entry, error)
}

// View is the synthesized directory-view surface: the overlay / local
// state, the fresh-list cache, and the pending-upload projection. Listing
// reads a finished view through this interface instead of reaching into
// the overlay's sources. VFS implements it via vfsListingView.
type View interface {
	// Overlay / local-state helpers.
	IsUnavailable(path string) bool
	IsDeleted(path string) bool
	FilterDeleted(parentPath string, entries []drive.Entry) []drive.Entry
	LocalChildren(parentPath string, entries []drive.Entry) []drive.Entry
	ApplyLocalModTimes(parentPath string, entries []drive.Entry) []drive.Entry
	UpdateOverlay(parentPath string, entries []drive.Entry)
	GetEntry(path string) (drive.Entry, bool)

	// View cache.
	FreshListCache(parentPath string, now time.Time) ([]drive.Entry, bool)
	CommitList(parentPath string, entries []drive.Entry, expires time.Time) []drive.Entry

	// Pending uploads feeding into listings.
	PendingUploads() []vfstypes.PendingUpload
}

// HealthRecorder receives listing-domain health statistics. It is optional:
// the listing domain works without one (a no-op sink), and VFS wires its
// health tracker through it so statistics never widen the host surface.
type HealthRecorder interface {
	RecordResult(op string, err error)
}

// noopHealth is the default HealthRecorder.
type noopHealth struct{}

func (noopHealth) RecordResult(string, error) {}

// ModTime returns the display mod time for a pending upload (zero when the
// record has no mtime set).
func ModTime(p vfstypes.PendingUpload) time.Time {
	if p.ModTime > 0 {
		return time.Unix(0, p.ModTime)
	}
	return time.Time{}
}
