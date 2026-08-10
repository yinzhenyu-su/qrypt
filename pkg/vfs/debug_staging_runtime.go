package vfs

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/yinzhenyu/qrypt/internal/vfs/diagnostics"
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
	return diagnostics.StagingMount(name, path, newVFSDebugStagingRuntime(v))
}

type vfsDebugStagingRuntime struct {
	v *VFS
}

func newVFSDebugStagingRuntime(v *VFS) vfsDebugStagingRuntime {
	return vfsDebugStagingRuntime{v: v}
}

func (r vfsDebugStagingRuntime) PendingUploads() []PendingUpload {
	return r.v.uploads.Store().PendingUploads()
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
	return r.v.uploads.Store().StagingDir()
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
			modTime := info.ModTime()
			file.ModTime = &modTime
		}
		files = append(files, file)
	}
	return files, nil
}

// --- migrated from debug_upload.go ---
