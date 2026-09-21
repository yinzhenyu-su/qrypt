package vfs

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

func newStagingHashTestFS(t *testing.T) *VFS {
	t.Helper()
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// hashStagingFile computes the required hashes of a staging file directly, as
// the reference the tracker-provided hashes must match.
func hashStagingFile(t *testing.T, path string, algorithms []drive.HashAlgorithm) drive.SourceHashes {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hashes, writers, err := upload.NewSnapshotHashes(algorithms)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.MultiWriter(writers...), f); err != nil {
		t.Fatal(err)
	}
	sums := make(drive.SourceHashes, len(hashes))
	for algorithm, h := range hashes {
		sums[algorithm] = h.Sum(nil)
	}
	return sums
}

func assertHashesEqual(t *testing.T, got, want drive.SourceHashes, algorithms []drive.HashAlgorithm, context string) {
	t.Helper()
	for _, algorithm := range algorithms {
		if string(got[algorithm]) != string(want[algorithm]) {
			t.Fatalf("%s: %s = %x, want %x", context, algorithm, got[algorithm], want[algorithm])
		}
	}
}

// TestStageExistingFeedsUploadSnapshotHashes pins that staging an existing
// remote file hashes the copied bytes on the way in: without it Flush cannot
// record SourceHashes and the upload snapshot re-reads the whole staging file.
func TestStageExistingFeedsUploadSnapshotHashes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newStagingHashTestFS(t)
	content := strings.Repeat("qrypt-staging-hash-", 8192)
	remote := &fakeUploadWriteRemote{
		entry: drive.Entry{ID: "remote", Name: "file.txt", Size: int64(len(content))},
		data:  content,
	}
	if err := fs.stageExistingWithDeps(ctx, "/file.txt", fs.uploads.Store(), remote); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/file.txt"); err != nil {
		t.Fatal(err)
	}
	pending, ok := fs.uploads.Store().UploadByPath("/file.txt")
	if !ok {
		t.Fatal("pending upload not recorded")
	}
	algorithms := requiredUploadSnapshotHashes(fs.driver)
	if !sourceHashesCover(pending.SourceHashes, algorithms) {
		t.Fatalf("pending source hashes incomplete after staging: %v (algorithms %v)", pending.SourceHashes, algorithms)
	}
	staged := hashStagingFile(t, pending.LocalPath, algorithms)
	assertHashesEqual(t, pending.SourceHashes, staged, algorithms, "flush hashes")

	snapshot, err := fs.snapshotPending(pending)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Incremental {
		t.Fatal("snapshot reported incremental hashes, want the staged hashes")
	}
	assertHashesEqual(t, snapshot.Hashes, staged, algorithms, "snapshot hashes")
}

// TestRotateFrozenGenerationFeedsHashes covers the copy that seeds staging
// content: rotating a frozen generation an upload has started reading. The new
// generation must hash the copied bytes, not re-read them at snapshot time.
func TestRotateFrozenGenerationFeedsHashes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newStagingHashTestFS(t)
	if _, err := fs.WriteAt(ctx, "/file.txt", []byte("hello world"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/file.txt"); err != nil {
		t.Fatal(err)
	}
	frozen, ok := fs.uploads.Store().UploadByPath("/file.txt")
	if !ok || !frozen.Frozen {
		t.Fatalf("first generation not frozen: %+v ok=%v", frozen, ok)
	}
	if _, ok := fs.uploads.Store().MarkUploadStarted(frozen); !ok {
		t.Fatal("frozen generation could not be claimed by an upload")
	}
	if _, err := fs.WriteAt(ctx, "/file.txt", []byte("!"), 11); err != nil {
		t.Fatal(err)
	}
	rotated, ok := fs.uploads.Store().UploadByPath("/file.txt")
	if !ok || rotated.FID == frozen.FID {
		t.Fatalf("write to a frozen pending did not rotate the generation: %+v", rotated)
	}
	if err := fs.Flush(ctx, "/file.txt"); err != nil {
		t.Fatal(err)
	}
	current, ok := fs.uploads.Store().UploadByPath("/file.txt")
	if !ok {
		t.Fatal("pending upload not recorded")
	}
	algorithms := requiredUploadSnapshotHashes(fs.driver)
	if !sourceHashesCover(current.SourceHashes, algorithms) {
		t.Fatalf("rotated generation has no incremental hashes: %v", current.SourceHashes)
	}
	staged := hashStagingFile(t, current.LocalPath, algorithms)
	assertHashesEqual(t, current.SourceHashes, staged, algorithms, "rotated flush hashes")
}

// TestStageExistingOutOfOrderWriteRehashesStaging covers the fallback: an
// out-of-sequence write after staging marks the tracker dirty, so the snapshot
// must re-read the file and report hashes of the final content.
func TestStageExistingOutOfOrderWriteRehashesStaging(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newStagingHashTestFS(t)
	content := strings.Repeat("qrypt-staging-hash-", 8192)
	remote := &fakeUploadWriteRemote{
		entry: drive.Entry{ID: "remote", Name: "file.txt", Size: int64(len(content))},
		data:  content,
	}
	if err := fs.stageExistingWithDeps(ctx, "/file.txt", fs.uploads.Store(), remote); err != nil {
		t.Fatal(err)
	}
	// Overwrite the first bytes in place: offset 0 is no longer the tracker's
	// next offset, so the incremental hashes no longer describe the file.
	if _, err := fs.WriteAt(ctx, "/file.txt", []byte("EDITED"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/file.txt"); err != nil {
		t.Fatal(err)
	}
	pending, ok := fs.uploads.Store().UploadByPath("/file.txt")
	if !ok {
		t.Fatal("pending upload not recorded")
	}
	algorithms := requiredUploadSnapshotHashes(fs.driver)
	if sourceHashesCover(pending.SourceHashes, algorithms) {
		t.Fatalf("flush kept stale incremental hashes after an out-of-order write: %v", pending.SourceHashes)
	}
	snapshot, err := fs.snapshotPending(pending)
	if err != nil {
		t.Fatal(err)
	}
	staged := hashStagingFile(t, pending.LocalPath, algorithms)
	assertHashesEqual(t, snapshot.Hashes, staged, algorithms, "snapshot hashes")
	// The edited content must be reflected: the hash cannot be the staged
	// original, or the fallback would be reporting pre-edit hashes.
	original := drive.NewBytesReadOnlyFileSource([]byte(content))
	originalSHA256, ok := drive.SourceHash(original, drive.HashSHA256)
	if !ok {
		t.Fatal("reference source lost SHA-256")
	}
	if string(snapshot.Hashes[drive.HashSHA256]) == string(originalSHA256) {
		t.Fatal("snapshot hashes still describe the pre-edit content")
	}
}
