package vfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (v *VFS) DebugStaging(ctx context.Context, path string) (DebugStagingReport, error) {
	path = cleanVirtual(path)
	mount := v.debugStagingMount(v.name, path)
	report := DebugStagingReport{Mounts: []DebugStagingMount{mount}}
	if path != "" && path != "/" {
		report.Path = path
	}
	return report, nil
}

func (v *VFS) debugStagingMount(name, path string) DebugStagingMount {
	return debugStagingMount(name, path, newVFSDebugStagingRuntime(v))
}

func debugStagingMount(name, path string, runtime debugStagingRuntime) DebugStagingMount {
	pending := runtime.PendingUploads()
	pendingByLocal := map[string]PendingUpload{}
	var pendingForPath *PendingUpload
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

func mergePendingStagingFile(file DebugStagingFile, pending PendingUpload, uploading, includeHash bool) DebugStagingFile {
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

func pendingStagingFile(pending PendingUpload, uploading, includeHash bool) DebugStagingFile {
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

func (n *Namespace) DebugStaging(ctx context.Context, path string) (DebugStagingReport, error) {
	path = cleanVirtual(path)
	report := DebugStagingReport{}
	if path != "/" {
		mount, rest, root, err := n.resolve(path)
		if err != nil {
			return DebugStagingReport{}, err
		}
		if root {
			return DebugStagingReport{Path: path}, nil
		}
		mountName := strings.Trim(strings.TrimPrefix(path, "/"), "/")
		if idx := strings.Index(mountName, "/"); idx >= 0 {
			mountName = mountName[:idx]
		}
		item := mount.debugStagingMount(mountName, rest)
		prefixStagingMountPaths(&item, mountName)
		report.Path = path
		report.Mounts = []DebugStagingMount{item}
		return report, nil
	}

	n.mu.RLock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		item := n.mounts[name].debugStagingMount(name, "/")
		prefixStagingMountPaths(&item, name)
		report.Mounts = append(report.Mounts, item)
	}
	n.mu.RUnlock()
	return report, nil
}

func prefixStagingMountPaths(mount *DebugStagingMount, mountName string) {
	for i := range mount.Files {
		if mount.Files[i].Path != "" {
			mount.Files[i].Path = joinVirtual("/"+mountName, strings.TrimPrefix(mount.Files[i].Path, "/"))
		}
	}
}

type debugStagingRuntime interface {
	PendingUploads() []PendingUpload
	UploadingPaths(pending []PendingUpload) map[string]bool
	StagingDir() string
	StagingFiles() ([]DebugStagingFile, error)
}

type vfsDebugStagingRuntime struct {
	v *VFS
}

func newVFSDebugStagingRuntime(v *VFS) vfsDebugStagingRuntime {
	return vfsDebugStagingRuntime{v: v}
}

func (r vfsDebugStagingRuntime) PendingUploads() []PendingUpload {
	return r.v.upload.store.PendingUploads()
}

func (r vfsDebugStagingRuntime) UploadingPaths(pending []PendingUpload) map[string]bool {
	uploading := map[string]bool{}
	for _, upload := range r.v.uploadSnapshots(pending) {
		if upload.State == uploadSnapshotStateUploading {
			uploading[upload.Path] = true
		}
	}
	return uploading
}

func (r vfsDebugStagingRuntime) StagingDir() string {
	return r.v.upload.store.staging.dir
}

func (r vfsDebugStagingRuntime) StagingFiles() ([]DebugStagingFile, error) {
	entries, err := os.ReadDir(r.StagingDir())
	if err != nil {
		return nil, err
	}
	files := make([]DebugStagingFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".staging") {
			continue
		}
		localPath := filepath.Join(r.StagingDir(), entry.Name())
		info, statErr := entry.Info()
		file := DebugStagingFile{LocalPath: localPath, Exists: statErr == nil}
		if statErr != nil {
			file.Issue = statErr.Error()
		} else {
			file.StagingSize = info.Size()
			file.ModTime = ptrTime(info.ModTime())
		}
		files = append(files, file)
	}
	return files, nil
}
