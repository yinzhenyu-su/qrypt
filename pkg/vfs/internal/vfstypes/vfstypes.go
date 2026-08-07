// Package vfstypes holds data types shared between the VFS package and its
// internal sub-packages (upload, delete, cache, debug). These are plain
// data structs with no dependencies on VFS internals, so sub-packages can
// import them without creating cycles.
package vfstypes

import (
	"path/filepath"
	"strings"
)

// PendingUpload is a file staged for upload but not yet committed.
type PendingUpload struct {
	Path          string               `json:"path"`
	FID           string               `json:"fid"`
	ParentID      string               `json:"parent_id"`
	Name          string               `json:"name"`
	LocalPath     string               `json:"local_path"`
	Size          int64                `json:"size"`
	ModTime       int64                `json:"mod_time,omitempty"`
	UpdatedAt     int64                `json:"updated_at"`
	RetryCount    int                  `json:"retry_count,omitempty"`
	LastError     string               `json:"last_error,omitempty"`
	PermanentFail bool                 `json:"permanent_fail,omitempty"`
	LastAttemptAt int64                `json:"last_attempt_at,omitempty"`
	NextAttemptAt int64                `json:"next_attempt_at,omitempty"`
	ReplaceUpload *UploadReplacement   `json:"replace_upload,omitempty"`
	Staging       *UploadStagingStatus `json:"staging,omitempty"`
	Frozen        bool                 `json:"frozen,omitempty"`
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
	path = filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(path, "/")))
	if path == "." {
		return "/"
	}
	return path
}

// IsPathUnder reports whether path is strictly inside dir (not equal).
func IsPathUnder(path, dir string) bool {
	path = CleanVirtualPath(path)
	dir = CleanVirtualPath(dir)
	return dir != "/" && len(path) > len(dir) && path[:len(dir)] == dir && path[len(dir)] == '/'
}
