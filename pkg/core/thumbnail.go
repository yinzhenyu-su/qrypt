package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/core/internal/thumbnail"
)

// ThumbnailInfo is the client DTO for a thumbnail cache probe or write.
// Hit=false means the cache has no entry for the source+preset key, in
// which case the other fields are empty except Preset (the normalized
// preset, whitespace-trimmed).
type ThumbnailInfo struct {
	Hit    bool   `json:"hit"`
	Path   string `json:"path,omitempty"`
	Mime   string `json:"mime,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Preset string `json:"preset,omitempty"`
}

// GetThumbnailFile probes the thumbnail cache for sourcePath and preset.
// A miss returns Hit=false without error; a cancelled context or a stat
// failure on the source file propagate their error.
func (c *Core) GetThumbnailFile(ctx context.Context, sourcePath, preset string) (ThumbnailInfo, error) {
	if c == nil || c.fs == nil {
		return ThumbnailInfo{}, fmt.Errorf("core: closed")
	}
	info, err := thumbnail.Get(ctx, c.thumbnailServiceConfig(), thumbnailSource{c.fs}, sourcePath, preset)
	if err != nil {
		return ThumbnailInfo{}, err
	}
	return toThumbnailInfo(info), nil
}

// PutThumbnailFile copies the local thumbnail file at localPath into the
// cache for sourcePath and preset, stores its MIME and size in the cache
// metadata, and prunes the cache to the configured budget.
func (c *Core) PutThumbnailFile(ctx context.Context, sourcePath, preset, mime, localPath string) (ThumbnailInfo, error) {
	if c == nil || c.fs == nil {
		return ThumbnailInfo{}, fmt.Errorf("core: closed")
	}
	if strings.TrimSpace(localPath) == "" {
		return ThumbnailInfo{}, fmt.Errorf("core: thumbnail local path required")
	}
	info, err := thumbnail.Put(ctx, c.thumbnailServiceConfig(), thumbnailSource{c.fs}, sourcePath, preset, mime, localPath)
	if err != nil {
		return ThumbnailInfo{}, err
	}
	return toThumbnailInfo(info), nil
}

// thumbnailServiceConfig passes the runtime-resolved cache configuration to
// the service; the fields stay owned by Core (runtime composition).
func (c *Core) thumbnailServiceConfig() thumbnail.Config {
	return thumbnail.Config{Dir: c.thumbnailDir, MaxBytes: c.thumbnailMax}
}

// thumbnailSource adapts the filesystem facade to the narrow Source
// interface the service needs, mapping the filesystem's entry type onto the
// service's value type.
type thumbnailSource struct {
	fs BuiltFileSystem
}

func (s thumbnailSource) Stat(ctx context.Context, path string) (thumbnail.Entry, error) {
	item, err := s.fs.Stat(ctx, path)
	if err != nil {
		return thumbnail.Entry{}, err
	}
	return thumbnail.Entry{
		ID:        item.ID,
		IsDir:     item.IsDir,
		Size:      item.Size,
		ModTime:   item.ModTime,
		UpdatedAt: item.UpdatedAt,
	}, nil
}

// toThumbnailInfo converts the service result to the public client DTO.
func toThumbnailInfo(info thumbnail.Info) ThumbnailInfo {
	return ThumbnailInfo{
		Hit:    info.Hit,
		Path:   info.Path,
		Mime:   info.Mime,
		Size:   info.Size,
		Preset: info.Preset,
	}
}
