package vfs

import (
	"path/filepath"

	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Type aliases for shared data types (implementations in pkg/vfs/vfstypes).
type PendingUpload = vfstypes.PendingUpload

// debugActiveOp aliases the in-flight operation descriptor (pkg/vfs/vfstypes).
type debugActiveOp = vfstypes.DebugActiveOp

// readCacheStore aliases the durable read-cache store (pkg/vfs/readcache).
type readCacheStore = readcache.Store

// readCacheLargeFileBytes marks files that are too large to seed into the
// read cache from upload staging.
var readCacheLargeFileBytes int64 = readcache.ReadCacheLargeFileBytes

type stores struct {
	*readCacheStore
	*uploadStore
}

func newStoresInDir(dir string, maxSize int64) (*stores, error) {
	return newStores(dir, filepath.Join(dir, "reading"), maxSize)
}
func newStores(uploadDir, readCacheDir string, maxSize int64) (*stores, error) {
	readCache, err := readcache.NewStore(readCacheDir, maxSize)
	if err != nil {
		return nil, err
	}
	uploads, err := newUploadStore(uploadDir)
	if err != nil {
		_ = readCache.Close()
		return nil, err
	}
	return &stores{readCacheStore: readCache, uploadStore: uploads}, nil
}
func newUploadStore(dir string) (*uploadStore, error) {
	return upload.NewPendingStore(dir)
}
