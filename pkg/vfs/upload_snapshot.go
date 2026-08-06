package vfs

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"hash"
	"io"
	"os"
	"sort"
	"time"
)

type uploadSnapshot struct {
	Path        string
	Hashes      drive.SourceHashes
	Incremental bool
}

type vfsUploadSnapshotter struct {
	v *VFS
}

func newVFSUploadSnapshotter(v *VFS) vfsUploadSnapshotter {
	return vfsUploadSnapshotter{v: v}
}

func (s vfsUploadSnapshotter) SnapshotPending(pending PendingUpload) (uploadSnapshot, error) {
	return s.v.snapshotPending(pending)
}

func (v *VFS) snapshotPending(pending PendingUpload) (uploadSnapshot, error) {
	unlock := v.lockPath(pending.Path)
	defer unlock()
	if err := v.upload.store.staging.sync(pending.LocalPath); err != nil {
		return uploadSnapshot{}, err
	}
	info, err := os.Stat(pending.LocalPath)
	if err != nil {
		return uploadSnapshot{}, err
	}
	if info.Size() != pending.Size {
		return uploadSnapshot{}, fmt.Errorf("vfs: pending changed during upload snapshot: file has %d, expected %d", info.Size(), pending.Size)
	}
	algorithms := v.requiredUploadSnapshotHashes()
	if hashes, ok := v.upload.hashes.snapshot(pending, algorithms); ok {
		return uploadSnapshot{
			Path:        pending.LocalPath,
			Hashes:      hashes,
			Incremental: true,
		}, nil
	}
	src, err := os.Open(pending.LocalPath)
	if err != nil {
		return uploadSnapshot{}, err
	}
	defer src.Close()
	hashes, writers, err := newUploadSnapshotHashes(algorithms)
	if err != nil {
		return uploadSnapshot{}, err
	}
	if _, err := io.Copy(io.MultiWriter(writers...), src); err != nil {
		return uploadSnapshot{}, err
	}
	sums := make(drive.SourceHashes, len(hashes))
	for algorithm, h := range hashes {
		sums[algorithm] = h.Sum(nil)
	}
	return uploadSnapshot{
		Path:   pending.LocalPath,
		Hashes: sums,
	}, nil
}

func (v *VFS) requiredUploadSnapshotHashes() []drive.HashAlgorithm {
	required := []drive.HashAlgorithm{drive.HashSHA256}
	if v != nil {
		required = append(required, newVFSDriverRuntime(v).RequiredUploadHashes()...)
	}
	seen := make(map[drive.HashAlgorithm]bool, len(required))
	algorithms := make([]drive.HashAlgorithm, 0, len(required))
	for _, algorithm := range required {
		if algorithm == "" || seen[algorithm] {
			continue
		}
		seen[algorithm] = true
		algorithms = append(algorithms, algorithm)
	}
	return algorithms
}

func newUploadSnapshotHashes(algorithms []drive.HashAlgorithm) (map[drive.HashAlgorithm]hash.Hash, []io.Writer, error) {
	hashes := make(map[drive.HashAlgorithm]hash.Hash, len(algorithms))
	writers := make([]io.Writer, 0, len(algorithms))
	for _, algorithm := range algorithms {
		var h hash.Hash
		switch algorithm {
		case drive.HashMD5:
			h = md5.New()
		case drive.HashSHA1:
			h = sha1.New()
		case drive.HashSHA256:
			h = sha256.New()
		default:
			return nil, nil, fmt.Errorf("vfs: unsupported upload hash algorithm %q", algorithm)
		}
		hashes[algorithm] = h
		writers = append(writers, h)
	}
	return hashes, writers, nil
}

func uploadSnapshotHashNames(hashes drive.SourceHashes) []string {
	names := make([]string, 0, len(hashes))
	for algorithm := range hashes {
		names = append(names, string(algorithm))
	}
	sort.Strings(names)
	return names
}

func (v *VFS) seedReadCacheFromStaging(entry drive.Entry, localPath string) {
	cacheKey := v.readCacheKey(entry)
	if cacheKey == "" || localPath == "" {
		return
	}
	if entry.Size >= readCacheLargeFileBytes {
		logging.L.DebugfEvery("vfs.read_cache_seed_skip_large", time.Second, "[VFS] skip read cache seed for large upload id=%q size=%d local=%q", entry.ID, entry.Size, localPath)
		return
	}
	if err := v.read.cache.PutLocalFile(cacheKey, entry.Size, localPath); err != nil {
		logging.L.Warnf("[VFS] read cache seed failed id=%q local=%q err=%v", entry.ID, localPath, err)
	}
}

// freezeSnapshot snapshots the pending local file and confirms the pending
// record is still current. ok=false means the upload was removed or
// superseded while snapshotting; staging is cleaned and the current pending
// is requeued if frozen.
func (e uploadEngine) freezeSnapshot(pending PendingUpload) (uploadSnapshot, bool, error) {
	observer := e.observer
	phaseStart := timeutil.Now()
	snapshot, err := e.snapshot.SnapshotPending(pending)
	hashNames := uploadSnapshotHashNames(snapshot.Hashes)
	hashSource := "snapshot"
	if snapshot.Incremental {
		hashSource = "incremental"
	}
	snapshotExtra := map[string]any{"hashes": hashNames, "hash_source": hashSource}
	if err != nil {
		snapshotExtra["error"] = err.Error()
	}
	observer.Metadata(pending.Path, "", hashNames)
	observer.Event(pending.Path, "snapshot_hash", phaseStart, pending.Size, snapshotExtra)
	if err != nil {
		logging.L.Warnf("[VFS] upload snapshot failed path=%q local=%q err=%v", pending.Path, pending.LocalPath, err)
		return uploadSnapshot{}, false, err
	}
	if latest, ok := e.pending.UploadByPath(pending.Path); !ok {
		logging.L.DebugfEvery("vfs.skip_upload_removed_after_snapshot", time.Second, "[VFS] skip upload after snapshot; pending removed op_id=%q path=%q", pending.FID, pending.Path)
		e.pending.RemoveStagingIfUnreferenced(pending.LocalPath)
		return uploadSnapshot{}, false, nil
	} else if !sameUploadRecord(latest, pending) {
		logging.L.InfofEvery("vfs.upload_superseded_after_snapshot", time.Second, "[VFS] upload superseded after snapshot op_id=%q path=%q old_size=%d new_size=%d", pending.FID, pending.Path, pending.Size, latest.Size)
		e.pending.RemoveStagingIfUnreferenced(pending.LocalPath)
		e.runtime.RequeueIfFrozen(latest)
		return uploadSnapshot{}, false, nil
	}
	return snapshot, true, nil
}
