// Package vfstypes holds data types shared between the VFS package and its
// sub-packages (upload, delete, readcache, read, listing, debug). These
// are plain data structs with no dependencies on VFS internals, so
// sub-packages can import them without creating cycles.
package vfstypes

import (
	"errors"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	pathpkg "path"
	"strings"
	"time"
)

// PendingUpload is a file staged for upload but not yet committed.
type PendingUpload struct {
	Path          string `json:"path"`
	FID           string `json:"fid"`
	ParentID      string `json:"parent_id"`
	Name          string `json:"name"`
	LocalPath     string `json:"local_path"`
	Size          int64  `json:"size"`
	ModTime       int64  `json:"mod_time,omitempty"`
	UpdatedAt     int64  `json:"updated_at"`
	RetryCount    int    `json:"retry_count,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	PermanentFail bool   `json:"permanent_fail,omitempty"`
	LastAttemptAt int64  `json:"last_attempt_at,omitempty"`
	// NextAttemptAt is the retry deadline recorded with the failure: attempt
	// time plus backoff. It is a materialized diagnostic view for the pending
	// and task surfaces; scheduling derives the remaining wait from RetryCount
	// and LastAttemptAt instead, so a slow journal append cannot turn this
	// value into a second source of truth.
	NextAttemptAt int64                `json:"next_attempt_at,omitempty"`
	ReplaceUpload *UploadReplacement   `json:"replace_upload,omitempty"`
	SourceHashes  drive.SourceHashes   `json:"source_hashes,omitempty"`
	Staging       *UploadStagingStatus `json:"staging,omitempty"`
	Frozen        bool                 `json:"frozen,omitempty"`
	// UploadStarted marks that an upload has begun reading this generation's
	// staging file, which makes the bytes immutable for the reader. It stays in
	// memory: a crash leaves no upload in flight, so a recovered record reads
	// back as unclaimed and the write path may reuse its staging file. It is
	// also deliberately absent from SameUploadRecord - a requeued or retried
	// attempt of the same generation must still compare equal.
	UploadStarted bool `json:"-"`
	// AppliedAt is the monotonic in-process instant at which this record's
	// current state was applied. Scheduling measures the quiet window and the
	// retry backoff from it, so the journal append that made the change durable
	// is not subtracted from the wait, and a wall-clock (NTP) offset cannot
	// stretch or collapse it. It stays in memory, is absent from
	// SameUploadRecord, and is zero for records recovered from the journal -
	// those fall back to the wall-clock stamps below.
	AppliedAt time.Time `json:"-"`
	// RunNow carries an explicit user retry ("run this generation now"). It
	// skips the debounce and backoff floors and is cleared by the record's next
	// state change, so a failure or a new write goes back to normal scheduling.
	// It stays in memory: a restart is itself a long enough wait.
	RunNow bool `json:"-"`
}

// UploadReplacement holds the remote entry being replaced by a new upload.
type UploadReplacement struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
}

// UploadStagingStatus describes the on-disk staging file backing a pending upload.
type UploadStagingStatus struct {
	Exists      bool   `json:"exists"`
	Size        int64  `json:"size,omitempty"`
	SizeMatches bool   `json:"size_matches"`
	Error       string `json:"error,omitempty"`
	Path        string `json:"path,omitempty"`
}

// CleanVirtualPath normalizes qrypt virtual paths to absolute slash paths.
func CleanVirtualPath(path string) string {
	path = pathpkg.Clean("/" + strings.TrimPrefix(path, "/"))
	if path == "." {
		return "/"
	}
	return path
}

// IsNotFound reports whether err represents a missing virtual or remote
// path: the drive.ErrNotFound sentinel only (vfs.ErrNotFound aliases it).
func IsNotFound(err error) bool {
	return errors.Is(err, drive.ErrNotFound)
}

// CleanMountName normalizes a mount reference to its bare name.
func CleanMountName(name string) string {
	return strings.Trim(strings.TrimSpace(name), "/")
}

// IsPathUnder reports whether path is strictly inside dir (not equal).
func IsPathUnder(path, dir string) bool {
	path = CleanVirtualPath(path)
	dir = CleanVirtualPath(dir)
	return dir != "/" && len(path) > len(dir) && path[:len(dir)] == dir && path[len(dir)] == '/'
}

// JoinVirtualPath joins a cleaned parent virtual path and a child name.
func JoinVirtualPath(parent, name string) string {
	parent = CleanVirtualPath(parent)
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

// SplitVirtualPath splits a cleaned virtual path into the last component
// (name) and its parent directory. Unlike filepath.Dir/Base, this uses
// forward-slash semantics regardless of the host OS, which is required for
// virtual FUSE paths.
func SplitVirtualPath(p string) (name, parent string) {
	if p == "/" {
		return "/", "/"
	}
	idx := strings.LastIndexByte(p, '/')
	if idx <= 0 {
		return p[1:], "/"
	}
	return p[idx+1:], p[:idx]
}

// DebugActiveOp describes one in-flight operation for debug snapshots.
type DebugActiveOp struct {
	OpID                 string         `json:"op_id"`
	Kind                 string         `json:"kind"`
	Phase                string         `json:"phase,omitempty"`
	State                string         `json:"state"`
	Mount                string         `json:"mount,omitempty"`
	Path                 string         `json:"path,omitempty"`
	RemoteID             string         `json:"remote_id,omitempty"`
	Offset               int64          `json:"offset,omitempty"`
	Requested            int64          `json:"requested_bytes,omitempty"`
	ChunkIndex           int64          `json:"chunk_index,omitempty"`
	WindowStart          int64          `json:"window_start,omitempty"`
	WindowEnd            int64          `json:"window_end,omitempty"`
	Background           bool           `json:"background,omitempty"`
	WaitFor              string         `json:"wait_for,omitempty"`
	ReadIntent           string         `json:"read_intent,omitempty"`
	ReadStreams          int            `json:"read_streams,omitempty"`
	PrefetchCancelReason string         `json:"prefetch_cancel_reason,omitempty"`
	StartedAt            time.Time      `json:"started_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
	AgeMS                int64          `json:"age_ms"`
	Extra                map[string]any `json:"extra,omitempty"`
}
