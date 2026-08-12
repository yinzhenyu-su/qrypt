package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

type ThumbnailInfo struct {
	Hit    bool   `json:"hit"`
	Path   string `json:"path,omitempty"`
	Mime   string `json:"mime,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Preset string `json:"preset,omitempty"`
}

type thumbnailMeta struct {
	Mime      string    `json:"mime"`
	Size      int64     `json:"size"`
	SourceKey string    `json:"source_key"`
	Preset    string    `json:"preset"`
	CreatedAt time.Time `json:"created_at"`
}

func (c *Core) GetThumbnailFile(ctx context.Context, sourcePath, preset string) (ThumbnailInfo, error) {
	if c == nil || c.fs == nil {
		return ThumbnailInfo{}, fmt.Errorf("core: closed")
	}
	dir, _, _, err := c.thumbnailCachePath(ctx, sourcePath, preset)
	if err != nil {
		return ThumbnailInfo{}, err
	}
	meta, err := readThumbnailMeta(filepath.Join(dir, "meta.json"))
	if os.IsNotExist(err) {
		return ThumbnailInfo{Hit: false, Preset: preset}, nil
	}
	if err != nil {
		return ThumbnailInfo{}, err
	}
	path := filepath.Join(dir, "thumbnail"+thumbnailExt(meta.Mime))
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return ThumbnailInfo{Hit: false, Preset: preset}, nil
	}
	if err != nil {
		return ThumbnailInfo{}, err
	}
	return ThumbnailInfo{Hit: true, Path: path, Mime: meta.Mime, Size: info.Size(), Preset: meta.Preset}, nil
}

func (c *Core) PutThumbnailFile(ctx context.Context, sourcePath, preset, mime, localPath string) (ThumbnailInfo, error) {
	if c == nil || c.fs == nil {
		return ThumbnailInfo{}, fmt.Errorf("core: closed")
	}
	if strings.TrimSpace(localPath) == "" {
		return ThumbnailInfo{}, fmt.Errorf("core: thumbnail local path required")
	}
	dir, sourceKey, preset, err := c.thumbnailCachePath(ctx, sourcePath, preset)
	if err != nil {
		return ThumbnailInfo{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ThumbnailInfo{}, err
	}
	if err := removeExistingThumbnailFiles(dir); err != nil {
		return ThumbnailInfo{}, err
	}
	dst := filepath.Join(dir, "thumbnail"+thumbnailExt(mime))
	if err := copyFileAtomic(ctx, localPath, dst, 0o600); err != nil {
		return ThumbnailInfo{}, err
	}
	info, err := os.Stat(dst)
	if err != nil {
		return ThumbnailInfo{}, err
	}
	meta := thumbnailMeta{
		Mime:      strings.TrimSpace(mime),
		Size:      info.Size(),
		SourceKey: sourceKey,
		Preset:    preset,
		CreatedAt: time.Now().UTC(),
	}
	if err := writeThumbnailMeta(filepath.Join(dir, "meta.json"), meta); err != nil {
		return ThumbnailInfo{}, err
	}
	if err := c.pruneThumbnailCache(ctx, dir); err != nil {
		return ThumbnailInfo{}, err
	}
	return ThumbnailInfo{Hit: true, Path: dst, Mime: meta.Mime, Size: meta.Size, Preset: meta.Preset}, nil
}

func (c *Core) thumbnailCachePath(ctx context.Context, sourcePath, preset string) (string, string, string, error) {
	if c.thumbnailDir == "" {
		return "", "", "", fmt.Errorf("core: thumbnail cache unavailable")
	}
	preset = strings.TrimSpace(preset)
	if preset == "" {
		return "", "", "", fmt.Errorf("core: thumbnail preset required")
	}
	if strings.ContainsAny(preset, `/\`) || preset == "." || preset == ".." {
		return "", "", "", fmt.Errorf("core: invalid thumbnail preset %q", preset)
	}
	item, err := c.fs.Stat(ctx, sourcePath)
	if err != nil {
		return "", "", "", err
	}
	if item.IsDir {
		return "", "", "", fmt.Errorf("core: %s is a directory", sourcePath)
	}
	sourceKey := thumbnailSourceKey(sourcePath, item)
	sum := sha256.Sum256([]byte(sourceKey + "\x00" + preset))
	key := hex.EncodeToString(sum[:])
	return filepath.Join(c.thumbnailDir, key[:2], key), sourceKey, preset, nil
}

func thumbnailSourceKey(sourcePath string, item drive.Entry) string {
	mod := item.ModTime
	if item.UpdatedAt.After(mod) {
		mod = item.UpdatedAt
	}
	id := item.ID
	if id == "" {
		id = sourcePath
	}
	return id + "\x00" + strconv.FormatInt(item.Size, 10) + "\x00" + strconv.FormatInt(mod.UTC().UnixNano(), 10)
}

func thumbnailExt(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

func removeExistingThumbnailFiles(dir string) error {
	for _, ext := range []string{".jpg", ".png", ".webp", ".bin"} {
		if err := os.Remove(filepath.Join(dir, "thumbnail"+ext)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func readThumbnailMeta(path string) (thumbnailMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return thumbnailMeta{}, err
	}
	var meta thumbnailMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return thumbnailMeta{}, err
	}
	return meta, nil
}

func writeThumbnailMeta(path string, meta thumbnailMeta) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return util.WriteAtomicWithOptions(path, util.AtomicWriteOptions{
		Pattern: ".meta.json-*",
		Mode:    0o600,
		Replace: true,
	}, func(file *os.File) error {
		_, err := file.Write(raw)
		return err
	})
}

func copyFileAtomic(ctx context.Context, srcPath, dstPath string, perm os.FileMode) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	return util.WriteAtomicWithOptions(dstPath, util.AtomicWriteOptions{
		Pattern: ".thumbnail-*",
		Mode:    perm,
		Replace: true,
	}, func(dst *os.File) error {
		_, err := io.Copy(dst, contextReader{ctx: ctx, r: src})
		return err
	})
}

type thumbnailCacheEntry struct {
	dir     string
	bytes   int64
	modTime time.Time
}

func (c *Core) pruneThumbnailCache(ctx context.Context, keepDir string) error {
	if c.thumbnailMax <= 0 || c.thumbnailDir == "" {
		return nil
	}
	entries, total, err := thumbnailCacheEntries(ctx, c.thumbnailDir)
	if err != nil {
		return err
	}
	if total <= c.thumbnailMax {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.Before(entries[j].modTime)
	})
	for _, entry := range entries {
		if total <= c.thumbnailMax {
			break
		}
		if samePath(entry.dir, keepDir) {
			continue
		}
		if err := os.RemoveAll(entry.dir); err != nil {
			return err
		}
		total -= entry.bytes
	}
	return nil
}

func thumbnailCacheEntries(ctx context.Context, root string) ([]thumbnailCacheEntry, int64, error) {
	shards, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var entries []thumbnailCacheEntry
	var total int64
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		dirs, err := os.ReadDir(filepath.Join(root, shard.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, 0, err
		}
		for _, dir := range dirs {
			if !dir.IsDir() {
				continue
			}
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			default:
			}
			path := filepath.Join(root, shard.Name(), dir.Name())
			bytes, err := dirSize(ctx, path)
			if err != nil {
				return nil, 0, err
			}
			info, err := dir.Info()
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, 0, err
			}
			total += bytes
			entries = append(entries, thumbnailCacheEntry{dir: path, bytes: bytes, modTime: info.ModTime()})
		}
	}
	return entries, total, nil
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && aa == bb
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
	}
	return r.r.Read(p)
}
