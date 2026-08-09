package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
)

// StagingRuntime is the staging diagnostic surface (consumer side).
type StagingRuntime interface {
	PendingUploads() []vfstypes.PendingUpload
	UploadingPaths(pending []vfstypes.PendingUpload) map[string]bool
	StagingDir() string
	StagingFiles() ([]DebugStagingFile, error)
}

// StagingMount assembles one mount's staging report: pending records
// reconciled with on-disk staging files, orphans and size mismatches.
func StagingMount(name, path string, runtime StagingRuntime) DebugStagingMount {
	pending := runtime.PendingUploads()
	pendingByLocal := map[string]vfstypes.PendingUpload{}
	var pendingForPath *vfstypes.PendingUpload
	for _, item := range pending {
		pendingByLocal[item.LocalPath] = item
		if path != "" && path != "/" && item.Path == path {
			p := item
			pendingForPath = &p
		}
	}
	uploading := runtime.UploadingPaths(pending)

	mount := DebugStagingMount{Mount: name, PendingCount: len(pending)}
	files, err := runtime.StagingFiles()
	if err != nil {
		mount.Orphans = append(mount.Orphans, DebugStagingFile{
			LocalPath: runtime.StagingDir(),
			Issue:     err.Error(),
		})
		return mount
	}
	for _, file := range files {
		localPath := file.LocalPath
		mount.Bytes += file.StagingSize
		mount.StagingCount++
		if item, ok := pendingByLocal[localPath]; ok {
			file = mergePendingStagingFile(file, item, uploading[item.Path], path != "" && path != "/" && item.Path == path)
			if path == "" || path == "/" || item.Path == path {
				mount.Files = append(mount.Files, file)
			}
			continue
		}
		file.Pending = false
		file.Issue = "not_referenced_by_pending"
		mount.OrphanCount++
		mount.Orphans = append(mount.Orphans, file)
	}
	if pendingForPath != nil {
		found := false
		for _, file := range mount.Files {
			if file.Path == pendingForPath.Path {
				found = true
				break
			}
		}
		if !found {
			mount.Files = append(mount.Files, pendingStagingFile(*pendingForPath, uploading[pendingForPath.Path], true))
		}
	} else if path != "" && path != "/" {
		mount.Files = nil
	}
	sort.Slice(mount.Files, func(i, j int) bool { return mount.Files[i].Path < mount.Files[j].Path })
	sort.Slice(mount.Orphans, func(i, j int) bool { return mount.Orphans[i].LocalPath < mount.Orphans[j].LocalPath })
	return mount
}

func mergePendingStagingFile(file DebugStagingFile, pending vfstypes.PendingUpload, uploading, includeHash bool) DebugStagingFile {
	file.Path = pending.Path
	file.Pending = true
	file.PendingSize = pending.Size
	file.SizeMatches = file.Exists && file.StagingSize == pending.Size
	file.UploadInProgress = uploading
	file.LastError = pending.LastError
	if includeHash && file.Exists {
		if sum, err := fileSHA256(file.LocalPath); err == nil {
			file.SHA256 = sum
		} else {
			file.Issue = err.Error()
		}
	}
	return file
}

func pendingStagingFile(pending vfstypes.PendingUpload, uploading, includeHash bool) DebugStagingFile {
	file := DebugStagingFile{
		Path:             pending.Path,
		LocalPath:        pending.LocalPath,
		Pending:          true,
		PendingSize:      pending.Size,
		UploadInProgress: uploading,
		LastError:        pending.LastError,
	}
	info, err := os.Stat(pending.LocalPath)
	if err != nil {
		file.Issue = err.Error()
		return file
	}
	file.Exists = true
	file.StagingSize = info.Size()
	file.SizeMatches = file.StagingSize == pending.Size
	file.ModTime = ptrTime(info.ModTime())
	if includeHash {
		if sum, err := fileSHA256(pending.LocalPath); err == nil {
			file.SHA256 = sum
		} else {
			file.Issue = err.Error()
		}
	}
	return file
}

// PrefixStagingMountPaths prefixes mount-relative paths with the mount
// name (namespace-level reports).
func PrefixStagingMountPaths(mount *DebugStagingMount, mountName string) {
	for i := range mount.Files {
		if mount.Files[i].Path != "" {
			mount.Files[i].Path = joinVirtual("/"+mountName, strings.TrimPrefix(mount.Files[i].Path, "/"))
		}
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func joinVirtual(parent, name string) string {
	if parent == "" || parent == "/" {
		return "/" + strings.TrimPrefix(name, "/")
	}
	return strings.TrimSuffix(parent, "/") + "/" + strings.TrimPrefix(name, "/")
}
