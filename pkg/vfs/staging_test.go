package vfs

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"github.com/yinzhenyu/qrypt/internal/vfs/read"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestRotateFrozenGenerationCopiesContent(t *testing.T) {
	cache, err := NewStoresInDir(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	oldLocal, err := cache.uploadStore.CreateStaging("old-fid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.uploadStore.WriteStagingAt(oldLocal, []byte("hello world"), 0); err != nil {
		t.Fatal(err)
	}
	old := PendingUpload{Path: "/file", FID: "old-fid", ParentID: "0", Name: "file", LocalPath: oldLocal, Size: 11, Frozen: true}
	if err := cache.SaveUpload(old); err != nil {
		t.Fatal(err)
	}
	v := &VFS{
		read:      read.NewState(cache.readCacheStore),
		uploads:   newUploadService(cache.uploadStore, Options{}, nil, newUploadHashTrackerState()),
		pathLocks: newPathLockState(),
		view:      newViewState("0", time.Now()),
	}
	next, err := v.rotateFrozenGeneration("/file", old)
	if err != nil {
		t.Fatal(err)
	}
	if next.FID == old.FID || next.LocalPath == old.LocalPath {
		t.Fatalf("rotation did not create a new generation: %+v", next)
	}
	if next.Frozen {
		t.Fatal("new generation must be mutable")
	}
	if next.Size != 11 {
		t.Fatalf("new generation size = %d, want 11", next.Size)
	}
	data, err := os.ReadFile(next.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Fatalf("new generation content = %q, want %q", data, "hello world")
	}
	latest, ok := cache.UploadByPath("/file")
	if !ok || latest.FID != next.FID {
		t.Fatalf("pending not swapped to new generation: %+v ok=%v", latest, ok)
	}
	if _, err := os.Stat(oldLocal); err != nil {
		t.Fatalf("old frozen staging must survive rotation: %v", err)
	}
}

func TestRotateFrozenGenerationFailureKeepsOldPending(t *testing.T) {
	cache, err := NewStoresInDir(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	v := &VFS{
		read:      read.NewState(cache.readCacheStore),
		uploads:   newUploadService(cache.uploadStore, Options{}, nil, newUploadHashTrackerState()),
		pathLocks: newPathLockState(),
		view:      newViewState("0", time.Now()),
	}
	old := PendingUpload{
		Path: "/file", FID: "old-fid", ParentID: "0", Name: "file",
		LocalPath: t.TempDir(),
		Size:      10, Frozen: true,
	}
	if err := cache.SaveUpload(old); err != nil {
		t.Fatal(err)
	}
	if _, err := v.rotateFrozenGeneration("/file", old); err == nil {
		t.Fatal("rotateFrozenGeneration succeeded with directory source, want error")
	}
	latest, ok := cache.UploadByPath("/file")
	if !ok {
		t.Fatal("old pending missing after failed rotation")
	}
	if latest.FID != old.FID || !latest.Frozen || latest.LocalPath != old.LocalPath {
		t.Fatalf("pending after failed rotation = %+v, want old frozen pending", latest)
	}
	entries, err := os.ReadDir(cache.uploadStore.StagingDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("staging files leaked after failed rotation: %v", names)
	}
}

func TestCreateReplacingMutablePendingRemovesOldStaging(t *testing.T) {
	ctx := context.Background()
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Create(ctx, "/file"); err != nil {
		t.Fatal(err)
	}
	old, ok := fs.uploads.Store().UploadByPath("/file")
	if !ok {
		t.Fatal("old pending missing")
	}
	oldLocal := old.LocalPath
	if _, err := fs.WriteAt(ctx, "/file", []byte("partial upload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Create(ctx, "/file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldLocal); !os.IsNotExist(err) {
		t.Fatalf("old mutable staging still exists, err=%v", err)
	}
	latest, ok := fs.uploads.Store().UploadByPath("/file")
	if !ok {
		t.Fatal("new pending missing")
	}
	if latest.LocalPath == oldLocal || latest.Frozen {
		t.Fatalf("latest pending = %+v, want new mutable generation", latest)
	}
	if _, err := os.Stat(latest.LocalPath); err != nil {
		t.Fatalf("new staging missing: %v", err)
	}
}

func TestCreateReplacingFrozenPendingKeepsOldStaging(t *testing.T) {
	ctx := context.Background()
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Create(ctx, "/file"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/file", []byte("ready upload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/file"); err != nil {
		t.Fatal(err)
	}
	old, ok := fs.uploads.Store().UploadByPath("/file")
	if !ok {
		t.Fatal("old pending missing")
	}
	if !old.Frozen {
		t.Fatalf("old pending = %+v, want frozen", old)
	}
	if err := fs.Create(ctx, "/file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.LocalPath); err != nil {
		t.Fatalf("old frozen staging removed: %v", err)
	}
	latest, ok := fs.uploads.Store().UploadByPath("/file")
	if !ok {
		t.Fatal("new pending missing")
	}
	if latest.LocalPath == old.LocalPath || latest.Frozen {
		t.Fatalf("latest pending = %+v, want new mutable generation", latest)
	}
}

func TestSweepUnreferencedStaging(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewStoresInDir(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	live, err := cache.uploadStore.CreateStaging("live-fid")
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveUpload(PendingUpload{Path: "/live", FID: "live-fid", LocalPath: live}); err != nil {
		t.Fatal(err)
	}
	stagingDir := filepath.Join(dir, "staging")
	if err := os.WriteFile(filepath.Join(stagingDir, "orphan-fid.staging"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "note.txt"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStoresInDir(dir, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("referenced staging removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagingDir, "orphan-fid.staging")); !os.IsNotExist(err) {
		t.Fatalf("orphan staging not swept, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(stagingDir, "note.txt")); err != nil {
		t.Fatalf("non-staging file removed: %v", err)
	}
}

func TestSnapshotPendingReturnsStagingPathDirectly(t *testing.T) {
	cache, err := NewStoresInDir(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	v := &VFS{
		read:      read.NewState(cache.readCacheStore),
		uploads:   newUploadService(cache.uploadStore, Options{}, nil, newUploadHashTrackerState()),
		pathLocks: newPathLockState(),
		view:      newViewState("0", time.Now()),
	}
	localPath, err := cache.uploadStore.CreateStaging("file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.uploadStore.WriteStagingAt(localPath, []byte("snapshot-data"), 0); err != nil {
		t.Fatal(err)
	}
	pending := PendingUpload{Path: "/file", FID: "file", LocalPath: localPath, Size: int64(len("snapshot-data"))}

	snapshot, err := v.snapshotPending(pending)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Path != localPath {
		t.Fatalf("snapshot path = %q, want %q (staging file directly)", snapshot.Path, localPath)
	}
	if snapshot.Hashes == nil {
		t.Fatal("expected hashes to be computed")
	}
	if len(snapshot.Hashes) != 1 {
		t.Fatalf("expected 1 hash, got %d", len(snapshot.Hashes))
	}
	wantSHA256 := sha256.Sum256([]byte("snapshot-data"))
	if got, ok := snapshot.Hashes[drive.HashSHA256]; !ok || !bytes.Equal(got, wantSHA256[:]) {
		t.Fatalf("SHA256 metadata = %x, present=%v; want %x", got, ok, wantSHA256)
	}
	if _, ok := snapshot.Hashes[drive.HashMD5]; ok {
		t.Fatal("unexpected MD5 metadata without driver requirement")
	}
	if _, ok := snapshot.Hashes[drive.HashSHA1]; ok {
		t.Fatal("unexpected SHA1 metadata without driver requirement")
	}
}

func TestSnapshotPendingComputesDriverRequiredHashes(t *testing.T) {
	cache, err := NewStoresInDir(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("driver-required-hashes")
	drv := &snapshotHashDriver{
		Driver: drive.NewFakeDriver(),
		hashes: []drive.HashAlgorithm{drive.HashMD5, drive.HashSHA1, drive.HashSHA1},
	}
	v := &VFS{
		driver:    drv,
		read:      read.NewState(cache.readCacheStore),
		uploads:   newUploadService(cache.uploadStore, Options{}, nil, newUploadHashTrackerState()),
		pathLocks: newPathLockState(),
		view:      newViewState("0", time.Now()),
	}
	localPath, err := cache.uploadStore.CreateStaging("file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.uploadStore.WriteStagingAt(localPath, content, 0); err != nil {
		t.Fatal(err)
	}

	snapshot, err := v.snapshotPending(PendingUpload{Path: "/file", FID: "file", LocalPath: localPath, Size: int64(len(content))})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Hashes) != 3 {
		t.Fatalf("expected 3 hashes, got %d", len(snapshot.Hashes))
	}
	wantMD5 := md5.Sum(content)
	wantSHA1 := sha1.Sum(content)
	wantSHA256 := sha256.Sum256(content)
	for algorithm, want := range map[drive.HashAlgorithm][]byte{
		drive.HashMD5:    wantMD5[:],
		drive.HashSHA1:   wantSHA1[:],
		drive.HashSHA256: wantSHA256[:],
	} {
		if got, ok := snapshot.Hashes[algorithm]; !ok || !bytes.Equal(got, want) {
			t.Fatalf("%s metadata = %x, present=%v; want %x", algorithm, got, ok, want)
		}
	}
}

func TestSnapshotPendingUsesIncrementalHashesForSequentialWrite(t *testing.T) {
	ctx := context.Background()
	raw := drive.NewFakeDriver()
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}
	drv := &snapshotHashDriver{
		Driver: raw,
		hashes: []drive.HashAlgorithm{drive.HashMD5, drive.HashSHA1},
	}
	v, err := New(drv, Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Create(ctx, "/file"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteAt(ctx, "/file", []byte("hello "), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteAt(ctx, "/file", []byte("qrypt"), int64(len("hello "))); err != nil {
		t.Fatal(err)
	}
	pending, err := v.pendingUpload("/file")
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := v.snapshotPending(pending)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Incremental {
		t.Fatal("snapshot did not use incremental hashes")
	}
	content := []byte("hello qrypt")
	wantMD5 := md5.Sum(content)
	wantSHA1 := sha1.Sum(content)
	wantSHA256 := sha256.Sum256(content)
	for algorithm, want := range map[drive.HashAlgorithm][]byte{
		drive.HashMD5:    wantMD5[:],
		drive.HashSHA1:   wantSHA1[:],
		drive.HashSHA256: wantSHA256[:],
	} {
		if got, ok := snapshot.Hashes[algorithm]; !ok || !bytes.Equal(got, want) {
			t.Fatalf("%s metadata = %x, present=%v; want %x", algorithm, got, ok, want)
		}
	}
}

func TestSnapshotPendingFallsBackAfterNonSequentialWrite(t *testing.T) {
	ctx := context.Background()
	raw := drive.NewFakeDriver()
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := New(raw, Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Create(ctx, "/file"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteAt(ctx, "/file", []byte("abcdef"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteAt(ctx, "/file", []byte("XY"), 1); err != nil {
		t.Fatal(err)
	}
	pending, err := v.pendingUpload("/file")
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := v.snapshotPending(pending)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Incremental {
		t.Fatal("snapshot used incremental hashes after non-sequential write")
	}
	wantSHA256 := sha256.Sum256([]byte("aXYdef"))
	if got, ok := snapshot.Hashes[drive.HashSHA256]; !ok || !bytes.Equal(got, wantSHA256[:]) {
		t.Fatalf("SHA256 metadata = %x, present=%v; want %x", got, ok, wantSHA256)
	}
}

type snapshotHashDriver struct {
	drive.Driver
	hashes []drive.HashAlgorithm
}

func (d *snapshotHashDriver) RequiredUploadHashes() []drive.HashAlgorithm {
	return d.hashes
}

func TestPendingQuietWindowUsesLargeFileMinimum(t *testing.T) {
	store, _ := newUploadStore(t.TempDir())
	v := &VFS{uploads: newUploadService(store, Options{UploadDelay: 10 * time.Millisecond}, nil, newUploadHashTrackerState())}

	small := v.uploadQuietWindow(PendingUpload{Size: largeUploadQuietThreshold - 1})
	if small != 10*time.Millisecond {
		t.Fatalf("small quiet window = %s, want configured delay", small)
	}
	large := v.uploadQuietWindow(PendingUpload{Size: largeUploadQuietThreshold})
	if large != largeUploadQuietDelay {
		t.Fatalf("large quiet window = %s, want %s", large, largeUploadQuietDelay)
	}
}

func TestUploadAdmissionLargeUploadIsExclusive(t *testing.T) {
	small := PendingUpload{Path: "/small.txt", Size: largeUploadQuietThreshold - 1}
	large := PendingUpload{Path: "/large.bin", Size: largeUploadQuietThreshold}

	var admission uploadAdmission
	if !admission.TryAcquire(large, 3) {
		t.Fatal("large upload was not admitted")
	}
	if admission.TryAcquire(small, 3) {
		t.Fatal("small upload admitted while large upload is active")
	}
	if admission.TryAcquire(large, 3) {
		t.Fatal("second large upload admitted while large upload is active")
	}
	admission.Release(large)

	for i := range 3 {
		if !admission.TryAcquire(small, 3) {
			t.Fatalf("small upload %d was not admitted", i+1)
		}
	}
	if admission.TryAcquire(large, 3) {
		t.Fatal("large upload admitted while small uploads are active")
	}
	if admission.TryAcquire(small, 3) {
		t.Fatal("small upload admitted above worker count")
	}
	admission.Release(small)
	admission.Release(small)
	admission.Release(small)

	if !admission.TryAcquire(large, 3) {
		t.Fatal("large upload was not admitted after small uploads released")
	}
}
