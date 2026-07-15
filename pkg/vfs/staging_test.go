package vfs

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStagingCleanupUploadTempsKeepsPendingStaging(t *testing.T) {
	store, err := newStagingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(store.dir, "file.staging")
	remove := filepath.Join(store.dir, "file.staging.upload-123")
	other := filepath.Join(store.dir, "upload-123")
	for _, path := range []string{keep, remove, other} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if got := store.cleanupUploadTemps(); got != 1 {
		t.Fatalf("cleanupUploadTemps removed %d files, want 1", got)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("pending staging file removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unrelated file removed: %v", err)
	}
	if _, err := os.Stat(remove); !os.IsNotExist(err) {
		t.Fatalf("upload temp still exists, err=%v", err)
	}
}

func TestStagingCleanupUploadTempsForKeepsLatestSnapshot(t *testing.T) {
	store, err := newStagingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(store.dir, "file.staging")
	latest := filepath.Join(store.dir, "file.staging.upload-3")
	old := filepath.Join(store.dir, "file.staging.upload-1")
	other := filepath.Join(store.dir, "other.staging.upload-1")
	for _, path := range []string{staging, latest, old, other} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if got := store.cleanupUploadTempsFor(staging, latest); got != 1 {
		t.Fatalf("cleanupUploadTempsFor removed %d files, want 1", got)
	}
	for _, path := range []string{staging, latest, other} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to remain: %v", path, err)
		}
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old snapshot still exists, err=%v", err)
	}
}

func TestSnapshotPendingKeepsOnlyLatestUploadSnapshot(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	v := &VFS{
		cache:     cache,
		pathLocks: map[string]*sync.Mutex{},
	}
	localPath, err := cache.staging.create("file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.staging.writeAt(localPath, []byte("snapshot-data"), 0); err != nil {
		t.Fatal(err)
	}
	pending := PendingFile{Path: "/file", FID: "file", LocalPath: localPath, Size: int64(len("snapshot-data"))}
	old1 := localPath + ".upload-1"
	old2 := localPath + ".upload-2"
	for _, path := range []string{old1, old2} {
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	snapshot, err := v.snapshotPending(pending)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(snapshot.Path)

	entries, err := os.ReadDir(filepath.Dir(localPath))
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), filepath.Base(localPath)+".upload-") {
			snapshots = append(snapshots, filepath.Join(filepath.Dir(localPath), entry.Name()))
		}
	}
	if len(snapshots) != 1 || snapshots[0] != snapshot.Path {
		t.Fatalf("upload snapshots = %v, want only %s", snapshots, snapshot.Path)
	}
}

func TestPendingQuietWindowUsesLargeFileMinimum(t *testing.T) {
	v := &VFS{uploadDelay: 10 * time.Millisecond}

	small := v.pendingQuietWindow(PendingFile{Size: largeUploadQuietThreshold - 1})
	if small != 10*time.Millisecond {
		t.Fatalf("small quiet window = %s, want configured delay", small)
	}
	large := v.pendingQuietWindow(PendingFile{Size: largeUploadQuietThreshold})
	if large != largeUploadQuietDelay {
		t.Fatalf("large quiet window = %s, want %s", large, largeUploadQuietDelay)
	}
}

func TestUploadAdmissionLargeUploadIsExclusive(t *testing.T) {
	small := PendingFile{Path: "/small.txt", Size: largeUploadQuietThreshold - 1}
	large := PendingFile{Path: "/large.bin", Size: largeUploadQuietThreshold}

	var admission uploadAdmission
	if !admission.tryAcquire(large, 3) {
		t.Fatal("large upload was not admitted")
	}
	if admission.tryAcquire(small, 3) {
		t.Fatal("small upload admitted while large upload is active")
	}
	if admission.tryAcquire(large, 3) {
		t.Fatal("second large upload admitted while large upload is active")
	}
	admission.release(large)

	if !admission.tryAcquire(small, 3) || !admission.tryAcquire(small, 3) || !admission.tryAcquire(small, 3) {
		t.Fatal("small uploads should be admitted up to worker count")
	}
	if admission.tryAcquire(large, 3) {
		t.Fatal("large upload admitted while small uploads are active")
	}
	if admission.tryAcquire(small, 3) {
		t.Fatal("small upload admitted above worker count")
	}
	admission.release(small)
	admission.release(small)
	admission.release(small)

	if !admission.tryAcquire(large, 3) {
		t.Fatal("large upload was not admitted after small uploads released")
	}
}

func TestStagingSequentialSmallWritesDoNotUseWholeFilePage(t *testing.T) {
	store, err := newStagingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.create("large")
	if err != nil {
		t.Fatal(err)
	}

	chunk := make([]byte, 16*1024)
	total := int64(4 * 1024 * 1024)
	for off := int64(0); off < total; off += int64(len(chunk)) {
		n, err := store.writeAt(path, chunk, off)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(chunk) {
			t.Fatalf("writeAt wrote %d, want %d", n, len(chunk))
		}
	}

	if _, ok := store.pages.Load("large"); ok {
		t.Fatal("large sequential writes should not keep a whole-file staging page")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != total {
		t.Fatalf("staging size = %d, want %d", info.Size(), total)
	}
}
