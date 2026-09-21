package vfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

// stagingDirFiles lists the files of a mount's staging directory.
func stagingDirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

// TestWriteAfterFlushReusesStagingGeneration pins the cheap path for a write
// that follows a flush: while no upload has started reading the frozen
// generation, the write appends to the existing staging file instead of
// copying the whole file into a fresh generation. Every write+flush round used
// to pay that copy (16 rounds wrote 8.5x the logical bytes, 64 rounds 32.5x).
func TestWriteAfterFlushReusesStagingGeneration(t *testing.T) {
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
	before, err := os.ReadFile(frozen.LocalPath)
	if err != nil {
		t.Fatal(err)
	}

	appended := []byte(" again")
	if _, err := fs.WriteAt(ctx, "/file.txt", appended, int64(len(before))); err != nil {
		t.Fatal(err)
	}
	reused, ok := fs.uploads.Store().UploadByPath("/file.txt")
	if !ok {
		t.Fatal("pending upload not recorded")
	}
	if reused.FID != frozen.FID || reused.LocalPath != frozen.LocalPath {
		t.Fatalf("write to an unclaimed frozen generation rotated it: %+v -> %+v", frozen, reused)
	}
	if reused.Frozen {
		t.Fatal("reused generation stayed frozen")
	}
	staged, err := os.ReadFile(reused.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(staged) != string(before)+string(appended) {
		t.Fatalf("staged content = %q, want %q", staged, string(before)+string(appended))
	}
	if files := stagingDirFiles(t, filepath.Dir(reused.LocalPath)); len(files) != 1 {
		t.Fatalf("staging directory holds %d files after reuse, want only the reused generation: %v", len(files), files)
	}

	// The appended bytes must still hash correctly: the sequential tracker
	// continues across the reuse instead of the snapshot re-reading the file.
	if err := fs.Flush(ctx, "/file.txt"); err != nil {
		t.Fatal(err)
	}
	current, ok := fs.uploads.Store().UploadByPath("/file.txt")
	if !ok {
		t.Fatal("pending upload not recorded after reuse")
	}
	algorithms := requiredUploadSnapshotHashes(fs.driver)
	if !sourceHashesCover(current.SourceHashes, algorithms) {
		t.Fatalf("reused generation lost its incremental hashes: %v", current.SourceHashes)
	}
	assertHashesEqual(t, current.SourceHashes, hashStagingFile(t, current.LocalPath, algorithms), algorithms, "reused flush hashes")
}

// TestTruncateAfterFlushReusesStagingGeneration covers the same handoff on the
// truncate path, which shares the frozen-generation decision with writes.
func TestTruncateAfterFlushReusesStagingGeneration(t *testing.T) {
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
		t.Fatalf("generation not frozen after flush: %+v ok=%v", frozen, ok)
	}

	if err := fs.Truncate(ctx, "/file.txt", 5); err != nil {
		t.Fatal(err)
	}
	reused, ok := fs.uploads.Store().UploadByPath("/file.txt")
	if !ok {
		t.Fatal("pending upload not recorded")
	}
	if reused.FID != frozen.FID || reused.LocalPath != frozen.LocalPath {
		t.Fatalf("truncate of an unclaimed frozen generation rotated it: %+v -> %+v", frozen, reused)
	}
	staged, err := os.ReadFile(reused.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(staged) != "hello" {
		t.Fatalf("staged content after truncate = %q, want %q", staged, "hello")
	}
}

// TestReusedGenerationUploadsCombinedContent drives an upload on a generation
// that grew after its flush, so the reuse path is covered end to end: the
// staging file that is uploaded holds both writes and reaches the remote.
func TestReusedGenerationUploadsCombinedContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := New(localfs.New(remote), Options{
		StorageDir:  filepath.Join(t.TempDir(), "cache"),
		UploadDelay: time.Hour, // keep the worker away: the generation stays unclaimed
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/doc.txt", []byte("hello world"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/doc.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/doc.txt", []byte("qrypt"), 6); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/doc.txt"); err != nil {
		t.Fatal(err)
	}
	fs.uploads.Close()
	pending := fs.PendingUploads()
	if len(pending) != 1 {
		t.Fatalf("pending uploads = %+v, want one generation", pending)
	}
	if err := newUploadEngine(fs).Execute(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(remote, "doc.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello qrypt" {
		t.Fatalf("remote content = %q, want %q", data, "hello qrypt")
	}
}
