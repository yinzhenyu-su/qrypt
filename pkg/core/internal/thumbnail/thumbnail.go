// Package thumbnail implements the thumbnail cache service behind
// core.Core's GetThumbnailFile/PutThumbnailFile facades: it resolves a
// cache entry from a source file's identity plus a preset, persists the
// thumbnail bytes and metadata atomically, and prunes the cache back to
// the configured byte budget after every write.
//
// The package is an application service hidden behind core.Core and must
// not import pkg/core. The facade keeps runtime composition and converts
// the filesystem's entry type to the narrow Entry value; Config carries the
// resolved cache directory and budget in. Errors that reach a client keep
// the exact pkg/core namespace and wording ("core: ...") so the stable
// mobile JSON contract is unchanged. Filesystem errors from os.Stat,
// os.ReadFile and the atomic writers, and context errors, pass through
// unwrapped.
package thumbnail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/util"
)

// Config is the resolved thumbnail cache configuration for one core. The
// facade holds the values (runtime layout + config) and passes them in,
// so composition stays out of the service.
type Config struct {
	// Dir is the cache root. An empty Dir disables the cache.
	Dir string
	// MaxBytes is the prune budget; a non-positive MaxBytes disables
	// pruning.
	MaxBytes int64
}

// Source is the narrow filesystem surface the service needs to derive the
// cache key: the source file's identity, size, modification time and
// directory-ness. The facade adapts its filesystem's entry type to Entry.
type Source interface {
	Stat(ctx context.Context, path string) (Entry, error)
}

// Entry is the subset of a source-file stat the cache key uses.
type Entry struct {
	ID        string
	IsDir     bool
	Size      int64
	ModTime   time.Time
	UpdatedAt time.Time
}

// Info is the result of a cache lookup or write.
type Info struct {
	Hit    bool
	Path   string
	Mime   string
	Size   int64
	Preset string
}

// meta is the persisted per-entry metadata (meta.json on disk).
type meta struct {
	Mime      string    `json:"mime"`
	Size      int64     `json:"size"`
	SourceKey string    `json:"source_key"`
	Preset    string    `json:"preset"`
	CreatedAt time.Time `json:"created_at"`
}

// cacheEntry is one cache directory scanned during pruning.
type cacheEntry struct {
	dir     string
	bytes   int64
	modTime time.Time
}

// Get probes the cache for sourcePath and preset. A miss returns
// Info{Hit: false} with the trimmed preset and no error; validation and
// filesystem errors are returned unwrapped.
func Get(ctx context.Context, cfg Config, src Source, sourcePath, preset string) (Info, error) {
	dir, _, preset, err := cachePath(ctx, cfg.Dir, src, sourcePath, preset)
	if err != nil {
		return Info{}, err
	}
	meta, err := readMeta(filepath.Join(dir, "meta.json"))
	if os.IsNotExist(err) {
		return Info{Hit: false, Preset: preset}, nil
	}
	if err != nil {
		return Info{}, err
	}
	path := filepath.Join(dir, "thumbnail"+ext(meta.Mime))
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Info{Hit: false, Preset: preset}, nil
	}
	if err != nil {
		return Info{}, err
	}
	return Info{Hit: true, Path: path, Mime: meta.Mime, Size: info.Size(), Preset: meta.Preset}, nil
}

// Put copies the local thumbnail file at localPath into the cache for
// sourcePath and preset, then prunes the cache to cfg.MaxBytes. The caller
// (facade) has already rejected an empty localPath.
func Put(ctx context.Context, cfg Config, src Source, sourcePath, preset, mime, localPath string) (Info, error) {
	dir, sourceKey, preset, err := cachePath(ctx, cfg.Dir, src, sourcePath, preset)
	if err != nil {
		return Info{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Info{}, err
	}
	if err := removeExistingFiles(dir); err != nil {
		return Info{}, err
	}
	dst := filepath.Join(dir, "thumbnail"+ext(mime))
	if err := copyAtomic(ctx, localPath, dst, 0o600); err != nil {
		return Info{}, err
	}
	info, err := os.Stat(dst)
	if err != nil {
		return Info{}, err
	}
	meta := meta{
		Mime:      strings.TrimSpace(mime),
		Size:      info.Size(),
		SourceKey: sourceKey,
		Preset:    preset,
		CreatedAt: time.Now().UTC(),
	}
	if err := writeMeta(filepath.Join(dir, "meta.json"), meta); err != nil {
		return Info{}, err
	}
	if err := prune(ctx, cfg, dir); err != nil {
		return Info{}, err
	}
	return Info{Hit: true, Path: dst, Mime: meta.Mime, Size: meta.Size, Preset: meta.Preset}, nil
}

// cachePath validates the cache configuration and preset, stats the source
// file, and returns the cache directory for the source+preset key, the
// source key and the trimmed preset.
func cachePath(ctx context.Context, dir string, src Source, sourcePath, preset string) (string, string, string, error) {
	if dir == "" {
		return "", "", "", fmt.Errorf("core: thumbnail cache unavailable")
	}
	preset = strings.TrimSpace(preset)
	if preset == "" {
		return "", "", "", fmt.Errorf("core: thumbnail preset required")
	}
	if strings.ContainsAny(preset, `/\`) || preset == "." || preset == ".." {
		return "", "", "", fmt.Errorf("core: invalid thumbnail preset %q", preset)
	}
	item, err := src.Stat(ctx, sourcePath)
	if err != nil {
		return "", "", "", err
	}
	if item.IsDir {
		return "", "", "", fmt.Errorf("core: %s is a directory", sourcePath)
	}
	sourceKey := sourceKey(item, sourcePath)
	sum := sha256.Sum256([]byte(sourceKey + "\x00" + preset))
	key := hex.EncodeToString(sum[:])
	return filepath.Join(dir, key[:2], key), sourceKey, preset, nil
}

func sourceKey(item Entry, sourcePath string) string {
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

func ext(mime string) string {
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

func removeExistingFiles(dir string) error {
	for _, e := range []string{".jpg", ".png", ".webp", ".bin"} {
		if err := os.Remove(filepath.Join(dir, "thumbnail"+e)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func readMeta(path string) (meta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return meta{}, err
	}
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return meta{}, err
	}
	return m, nil
}

func writeMeta(path string, m meta) error {
	raw, err := json.Marshal(m)
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

func copyAtomic(ctx context.Context, srcPath, dstPath string, perm os.FileMode) error {
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

// prune removes the oldest cache entries until the total size fits
// cfg.MaxBytes. The entry containing keepDir is never removed. A
// non-positive MaxBytes or empty Dir disables pruning.
func prune(ctx context.Context, cfg Config, keepDir string) error {
	if cfg.MaxBytes <= 0 || cfg.Dir == "" {
		return nil
	}
	entries, total, err := cacheEntries(ctx, cfg.Dir)
	if err != nil {
		return err
	}
	if total <= cfg.MaxBytes {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.Before(entries[j].modTime)
	})
	for _, entry := range entries {
		if total <= cfg.MaxBytes {
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

func cacheEntries(ctx context.Context, root string) ([]cacheEntry, int64, error) {
	shards, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var entries []cacheEntry
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
			entries = append(entries, cacheEntry{dir: path, bytes: bytes, modTime: info.ModTime()})
		}
	}
	return entries, total, nil
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
