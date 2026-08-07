package vfs

import (
	"path/filepath"

	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/vfstypes"
)

// Type aliases for shared data types (implementations in internal/vfstypes).
type PendingUpload = vfstypes.PendingUpload
type UploadReplacement = vfstypes.UploadReplacement
type UploadStagingStatus = vfstypes.UploadStagingStatus

// readCacheStore aliases the durable read-cache store (internal/readcache).
type readCacheStore = readcache.Store

// DebugReadCache / DebugReadCacheFile are exported aliases for callers
// outside pkg/vfs (implementations live in internal/readcache).
type DebugReadCache = readcache.DebugReadCache
type DebugReadCacheFile = readcache.DebugReadCacheFile

// readCacheLargeFileBytes marks files that are too large to seed into the
// read cache from upload staging.
var readCacheLargeFileBytes int64 = readcache.ReadCacheLargeFileBytes

type Stores struct {
	*readCacheStore
	*uploadStore
}

func NewStoresInDir(dir string, maxSize int64) (*Stores, error) {
	return NewStores(dir, filepath.Join(dir, "reading"), maxSize)
}
func NewStores(uploadDir, readCacheDir string, maxSize int64) (*Stores, error) {
	readCache, err := readcache.NewStore(readCacheDir, maxSize)
	if err != nil {
		return nil, err
	}
	uploads, err := newUploadStore(uploadDir)
	if err != nil {
		_ = readCache.Close()
		return nil, err
	}
	return &Stores{readCacheStore: readCache, uploadStore: uploads}, nil
}
func newUploadStore(dir string) (*uploadStore, error) {
	return upload.NewPendingStore(dir)
}
