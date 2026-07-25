package core

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type StorageUsage struct {
	TotalBytes     int64               `json:"total_bytes"`
	CacheBytes     int64               `json:"cache_bytes"`
	ReadCacheBytes int64               `json:"read_cache_bytes"`
	ThumbnailBytes int64               `json:"thumbnail_cache_bytes"`
	StagingBytes   int64               `json:"staging_bytes"`
	ConfigBytes    int64               `json:"config_bytes,omitempty"`
	StateBytes     int64               `json:"state_bytes,omitempty"`
	LogBytes       int64               `json:"log_bytes,omitempty"`
	TmpBytes       int64               `json:"tmp_bytes,omitempty"`
	OtherBytes     int64               `json:"other_bytes,omitempty"`
	Mounts         []StorageMountUsage `json:"mounts,omitempty"`
}

type StorageMountUsage struct {
	Name           string `json:"name"`
	CacheBytes     int64  `json:"cache_bytes"`
	ReadCacheBytes int64  `json:"read_cache_bytes"`
	StagingBytes   int64  `json:"staging_bytes"`
	OtherBytes     int64  `json:"other_bytes,omitempty"`
}

func (c *Core) StorageUsage(ctx context.Context) (StorageUsage, error) {
	if c == nil || c.fs == nil {
		return StorageUsage{}, fmt.Errorf("core: closed")
	}
	usage := StorageUsage{}
	if c.readCacheDir != "" {
		cacheBytes, err := dirSize(ctx, c.readCacheDir)
		if err != nil {
			return StorageUsage{}, err
		}
		usage.ReadCacheBytes = cacheBytes
		usage.CacheBytes += cacheBytes
	}
	if c.thumbnailDir != "" {
		bytes, err := dirSize(ctx, c.thumbnailDir)
		if err != nil {
			return StorageUsage{}, err
		}
		usage.ThumbnailBytes = bytes
		usage.CacheBytes += bytes
	}
	if c.writebackDir != "" {
		writebackBytes, err := dirSize(ctx, c.writebackDir)
		if err != nil {
			return StorageUsage{}, err
		}
		mounts, stagingBytes, err := writebackMountUsage(ctx, c.readCacheDir, c.writebackDir)
		if err != nil {
			return StorageUsage{}, err
		}
		usage.Mounts = mounts
		usage.StagingBytes = stagingBytes
		usage.CacheBytes += writebackBytes
	}
	if c.runtimeLayout.ConfigDir != "" {
		bytes, err := dirSize(ctx, c.runtimeLayout.ConfigDir)
		if err != nil {
			return StorageUsage{}, err
		}
		usage.ConfigBytes = bytes
	}
	if c.runtimeLayout.StateDir != "" {
		bytes, err := dirSize(ctx, c.runtimeLayout.StateDir)
		if err != nil {
			return StorageUsage{}, err
		}
		usage.StateBytes = bytes
	}
	if c.runtimeLayout.LogDir != "" {
		bytes, err := dirSize(ctx, c.runtimeLayout.LogDir)
		if err != nil {
			return StorageUsage{}, err
		}
		usage.LogBytes = bytes
	}
	if c.runtimeLayout.TmpDir != "" {
		bytes, err := dirSize(ctx, c.runtimeLayout.TmpDir)
		if err != nil {
			return StorageUsage{}, err
		}
		usage.TmpBytes = bytes
	}
	if c.runtimeLayout.RootDir != "" {
		total, err := dirSize(ctx, c.runtimeLayout.RootDir)
		if err != nil {
			return StorageUsage{}, err
		}
		usage.TotalBytes = total
	} else {
		usage.TotalBytes = usage.CacheBytes + usage.ConfigBytes + usage.StateBytes + usage.LogBytes + usage.TmpBytes
	}
	known := usage.CacheBytes + usage.ConfigBytes + usage.StateBytes + usage.LogBytes + usage.TmpBytes
	if usage.TotalBytes > known {
		usage.OtherBytes = usage.TotalBytes - known
	}
	return usage, nil
}

func (c *Core) ClearReadCache(ctx context.Context) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	clearer, ok := c.fs.(interface {
		ClearReadCache() error
	})
	if !ok {
		return fmt.Errorf("core: read cache clear unavailable")
	}
	return clearer.ClearReadCache()
}

func (c *Core) ThumbnailCacheUsage(ctx context.Context) (int64, error) {
	if c == nil || c.fs == nil {
		return 0, fmt.Errorf("core: closed")
	}
	return dirSize(ctx, c.thumbnailDir)
}

func (c *Core) ClearThumbnailCache(ctx context.Context) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if c.thumbnailDir == "" {
		return fmt.Errorf("core: thumbnail cache unavailable")
	}
	if err := os.RemoveAll(c.thumbnailDir); err != nil {
		return err
	}
	return os.MkdirAll(c.thumbnailDir, 0o700)
}

func writebackMountUsage(ctx context.Context, readCacheDir, writebackDir string) ([]StorageMountUsage, int64, error) {
	entries, err := os.ReadDir(writebackDir)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	mounts := make([]StorageMountUsage, 0, len(entries))
	var totalStaging int64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		writebackMountDir := filepath.Join(writebackDir, entry.Name())
		writebackBytes, err := dirSize(ctx, writebackMountDir)
		if err != nil {
			return nil, 0, err
		}
		readBytes, err := dirSize(ctx, filepath.Join(readCacheDir, entry.Name()))
		if err != nil {
			return nil, 0, err
		}
		stagingBytes, err := dirSize(ctx, filepath.Join(writebackMountDir, "staging"))
		if err != nil {
			return nil, 0, err
		}
		item := StorageMountUsage{
			Name:           entry.Name(),
			CacheBytes:     readBytes + writebackBytes,
			ReadCacheBytes: readBytes,
			StagingBytes:   stagingBytes,
		}
		if item.CacheBytes > readBytes+stagingBytes {
			item.OtherBytes = item.CacheBytes - readBytes - stagingBytes
		}
		mounts = append(mounts, item)
		totalStaging += stagingBytes
	}
	return mounts, totalStaging, nil
}

func dirSize(ctx context.Context, path string) (int64, error) {
	if path == "" {
		return 0, nil
	}
	var total int64
	err := filepath.WalkDir(path, func(item string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}
