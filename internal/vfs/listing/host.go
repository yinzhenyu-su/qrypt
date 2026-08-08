// Package listing implements the VFS directory-listing domain: list
// coalescing, deterministic paging, and directory prefetch. It is driven
// by a Host interface so it stays free of VFS internals.
package listing

import (
	"context"
	"time"

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
	// CurrentDirectory returns the current visible entry for path, or
	// ok=false when the path is unavailable (deleted/hidden). Callers
	// combine the availability check and the identity lookup in one call
	// (used for list availability and prefetch stale-checks); check
	// entry.IsDir for directory identity.
	CurrentDirectory(path string) (drive.Entry, bool)

	// View cache.
	FreshListCache(parentPath string, now time.Time) ([]drive.Entry, bool)
	// CommitRemoteChildren folds a freshly fetched remote listing into the
	// synthesized view as a single semantic operation: rename/delete overlay
	// update, filtering of invisible remote nodes, local-modtime
	// application, entry/list-cache commit, and local-children merge -
	// returning the final effective listing. The view implementation owns
	// the ordered protocol (and its locks); the listing domain does not
	// orchestrate the internal steps.
	CommitRemoteChildren(parentPath string, remote []drive.Entry, expires time.Time) []drive.Entry
	// ProjectChildren applies the CURRENT visibility state to an already
	// fetched or cached entry snapshot: local modtimes, deleted/hidden
	// filtering, and local-children merge. It does not update the overlay,
	// does not touch the list cache, and does not mutate the input slice.
	ProjectChildren(parentPath string, entries []drive.Entry) []drive.Entry
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
