package diagnostics

import (
	"errors"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

type fakeDebugStagingRuntime struct {
	pending   []vfstypes.PendingUpload
	uploading map[string]bool
	dir       string
	files     []DebugStagingFile
	err       error
}

func (r *fakeDebugStagingRuntime) PendingUploads() []vfstypes.PendingUpload {
	return append([]vfstypes.PendingUpload(nil), r.pending...)
}

func (r *fakeDebugStagingRuntime) UploadingPaths([]vfstypes.PendingUpload) map[string]bool {
	out := map[string]bool{}
	for path, uploading := range r.uploading {
		out[path] = uploading
	}
	return out
}

func (r *fakeDebugStagingRuntime) StagingDir() string {
	return r.dir
}

func (r *fakeDebugStagingRuntime) StagingFiles() ([]DebugStagingFile, error) {
	return append([]DebugStagingFile(nil), r.files...), r.err
}

func TestDebugStagingMountUsesRuntimeData(t *testing.T) {
	modTime := time.Now()
	runtime := &fakeDebugStagingRuntime{
		pending: []vfstypes.PendingUpload{
			{Path: "/file.txt", FID: "file", LocalPath: "/cache/file.staging", Size: 4},
		},
		uploading: map[string]bool{"/file.txt": true},
		dir:       "/cache",
		files: []DebugStagingFile{
			{LocalPath: "/cache/file.staging", Exists: true, StagingSize: 4, ModTime: &modTime},
			{LocalPath: "/cache/orphan.staging", Exists: true, StagingSize: 2, ModTime: &modTime},
		},
	}
	mount := StagingMount("cloud", "/", runtime)
	if mount.PendingCount != 1 || mount.StagingCount != 2 || mount.OrphanCount != 1 || mount.Bytes != 6 {
		t.Fatalf("mount summary = %+v", mount)
	}
	if len(mount.Files) != 1 || mount.Files[0].Path != "/file.txt" || !mount.Files[0].UploadInProgress || !mount.Files[0].SizeMatches {
		t.Fatalf("files = %+v", mount.Files)
	}
	if len(mount.Orphans) != 1 || mount.Orphans[0].LocalPath != "/cache/orphan.staging" || mount.Orphans[0].Issue != "not_referenced_by_pending" {
		t.Fatalf("orphans = %+v", mount.Orphans)
	}
}

func TestDebugStagingMountReportsRuntimeReadError(t *testing.T) {
	runtime := &fakeDebugStagingRuntime{dir: "/cache", err: errors.New("cannot list")}
	mount := StagingMount("cloud", "/", runtime)
	if len(mount.Orphans) != 1 || mount.Orphans[0].LocalPath != "/cache" || mount.Orphans[0].Issue != "cannot list" {
		t.Fatalf("orphans = %+v", mount.Orphans)
	}
}
