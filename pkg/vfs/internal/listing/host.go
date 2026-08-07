// Package listing implements the VFS directory-listing domain: list
// coalescing, deterministic paging, and directory prefetch. It is driven
// by a Host interface so it stays free of VFS internals.
package listing

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/vfstypes"
)

// Host is the VFS surface the listing domain needs: resolution, the view
// overlay and cache, pending uploads, and health. VFS implements it via
// vfsListingHost.
type Host interface {
	Resolve(ctx context.Context, path string) (drive.Entry, error)
	RecordHealth(op string, err error)
	ListChildren(ctx context.Context, parentID string) ([]drive.Entry, error)

	// Overlay / local-state helpers.
	IsUnavailable(path string) bool
	IsDeleted(path string) bool
	FilterDeleted(parentPath string, entries []drive.Entry) []drive.Entry
	LocalChildren(parentPath string, entries []drive.Entry) []drive.Entry
	ApplyLocalModTimes(parentPath string, entries []drive.Entry) []drive.Entry
	ApplyLocalModTimeLocked(path string, entry drive.Entry) drive.Entry
	UpdateOverlay(parentPath string, entries []drive.Entry)
	GetEntry(path string) (drive.Entry, bool)

	// View cache.
	FreshListCache(parentPath string, now time.Time) ([]drive.Entry, bool)
	CommitList(parentPath string, entries []drive.Entry, expires time.Time) []drive.Entry

	// Pending uploads feeding into listings.
	PendingUploads() []vfstypes.PendingUpload
}

// ModTime returns the display mod time for a pending upload (zero when the
// record has no mtime set).
func ModTime(p vfstypes.PendingUpload) time.Time {
	if p.ModTime > 0 {
		return time.Unix(0, p.ModTime)
	}
	return time.Time{}
}
