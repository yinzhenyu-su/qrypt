package thumbnail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSource serves source-file stats from a map. By default it honours
// context cancellation up front (like a ctx-aware filesystem stat); tests
// that need the service's own cancellation paths set statIgnoresCtx.
type fakeSource struct {
	mu             sync.Mutex
	entries        map[string]Entry
	err            error
	statIgnoresCtx bool
}

func (f *fakeSource) Stat(ctx context.Context, path string) (Entry, error) {
	if !f.statIgnoresCtx {
		if err := ctx.Err(); err != nil {
			return Entry{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return Entry{}, f.err
	}
	entry, ok := f.entries[path]
	if !ok {
		return Entry{}, os.ErrNotExist
	}
	return entry, nil
}

// putOnce writes one thumbnail for a source and returns the service result
// plus the config used, so tests can measure cache dirs afterwards.
func putOnce(t *testing.T, cfg Config, src Source, sourcePath, preset, mime, content string) Info {
	t.Helper()
	local := filepath.Join(t.TempDir(), "thumb-local")
	if err := os.WriteFile(local, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := Put(context.Background(), cfg, src, sourcePath, preset, mime, local)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func defaultCfg(t *testing.T) Config {
	return Config{Dir: filepath.Join(t.TempDir(), "cache", "thumbnail"), MaxBytes: 1 << 30}
}

func checkThumbnailFile(t *testing.T, path, content string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cached thumbnail %s: %v", path, err)
	}
	if string(data) != content {
		t.Fatalf("cached thumbnail content = %q, want %q", string(data), content)
	}
}

func TestGetMissPutHitRoundtrip(t *testing.T) {
	ctx := context.Background()
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/photo.jpg": {ID: "photo-1", Size: 123, ModTime: time.Unix(100, 0).UTC()},
	}}
	preset := "grid-128"

	miss, err := Get(ctx, cfg, src, "/quark/photo.jpg", preset)
	if err != nil {
		t.Fatal(err)
	}
	if miss.Hit || miss.Path != "" || miss.Mime != "" || miss.Size != 0 {
		t.Fatalf("initial Get = %+v, want clean miss", miss)
	}
	if miss.Preset != preset {
		t.Fatalf("miss preset = %q, want %q", miss.Preset, preset)
	}

	put := putOnce(t, cfg, src, "/quark/photo.jpg", preset, "image/jpeg", "thumbnail")
	if !put.Hit || put.Mime != "image/jpeg" || put.Size != int64(len("thumbnail")) {
		t.Fatalf("Put = %+v", put)
	}
	if !strings.HasPrefix(put.Path, cfg.Dir) {
		t.Fatalf("Put path = %q, want cache dir prefix %q", put.Path, cfg.Dir)
	}
	checkThumbnailFile(t, put.Path, "thumbnail")

	hit, err := Get(ctx, cfg, src, "/quark/photo.jpg", preset)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Hit || hit.Path != put.Path || hit.Mime != "image/jpeg" || hit.Size != int64(len("thumbnail")) || hit.Preset != preset {
		t.Fatalf("Get after Put = %+v, want hit matching Put", hit)
	}
}

func TestGetMissOnStaleMetaWithoutThumbnail(t *testing.T) {
	ctx := context.Background()
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/photo.jpg": {ID: "photo-1", Size: 1, ModTime: time.Unix(100, 0).UTC()},
	}}
	// A meta.json without its thumbnail file must read as a miss, not an
	// error: the original facade treated the missing file as a miss.
	put := putOnce(t, cfg, src, "/quark/photo.jpg", "grid-128", "image/jpeg", "thumb")
	metaPath := filepath.Join(filepath.Dir(put.Path), "meta.json")
	if err := os.Remove(put.Path); err != nil {
		t.Fatal(err)
	}
	info, err := Get(ctx, cfg, src, "/quark/photo.jpg", "grid-128")
	if err != nil {
		t.Fatal(err)
	}
	if info.Hit {
		t.Fatalf("Get with stale meta = %+v, want miss", info)
	}
	if info.Preset != "grid-128" {
		t.Fatalf("miss preset = %q, want grid-128", info.Preset)
	}
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("meta.json should remain on a soft miss: %v", err)
	}
}

func TestPresetAndMimeNormalization(t *testing.T) {
	ctx := context.Background()
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/photo.jpg": {ID: "photo-1", Size: 1, ModTime: time.Unix(100, 0).UTC()},
	}}
	// Whitespace around the preset is trimmed before keying; the returned
	// fields carry the trimmed values so the client sees one canonical value.
	put := putOnce(t, cfg, src, "/quark/photo.jpg", "  grid-128  ", " IMAGE/JPEG ", "thumb")
	if put.Mime != "IMAGE/JPEG" {
		t.Fatalf("Put mime = %q, want trimmed original casing", put.Mime)
	}
	if !strings.HasSuffix(put.Path, ".jpg") {
		t.Fatalf("Put path = %q, want .jpg extension for image/jpeg", put.Path)
	}
	if put.Preset != "grid-128" {
		t.Fatalf("Put preset = %q, want trimmed grid-128", put.Preset)
	}
	hit, err := Get(ctx, cfg, src, "/quark/photo.jpg", "   grid-128  ")
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Hit || hit.Preset != "grid-128" || hit.Mime != "IMAGE/JPEG" {
		t.Fatalf("Get with padded preset = %+v, want hit on trimmed key", hit)
	}
}

func TestUnknownMimeFallsBackToBinExtension(t *testing.T) {
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/photo.jpg": {ID: "photo-1", Size: 1, ModTime: time.Unix(100, 0).UTC()},
	}}
	put := putOnce(t, cfg, src, "/quark/photo.jpg", "grid-128", "application/octet-stream", "thumb")
	if !strings.HasSuffix(put.Path, ".bin") {
		t.Fatalf("Put path = %q, want .bin fallback extension", put.Path)
	}
	if put.Mime != "application/octet-stream" {
		t.Fatalf("Put mime = %q, want stored as given", put.Mime)
	}
	files, err := os.ReadDir(filepath.Dir(put.Path))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name() != "thumbnail.bin" && f.Name() != "meta.json" {
			t.Fatalf("unexpected cache file %q in entry dir", f.Name())
		}
	}
}

func TestCacheKeyVariesByPresetAndSource(t *testing.T) {
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/a.jpg": {ID: "a-1", Size: 10, ModTime: time.Unix(100, 0).UTC()},
		"/quark/b.jpg": {ID: "b-1", Size: 20, ModTime: time.Unix(200, 0).UTC()},
	}}
	gridA := putOnce(t, cfg, src, "/quark/a.jpg", "grid-128", "image/jpeg", "thumb-a")
	gridB := putOnce(t, cfg, src, "/quark/b.jpg", "grid-128", "image/jpeg", "thumb-b")
	if gridA.Path == gridB.Path {
		t.Fatalf("different sources shared a cache entry: %q", gridA.Path)
	}
	smallA := putOnce(t, cfg, src, "/quark/a.jpg", "small-32", "image/png", "thumb-a")
	if gridA.Path == smallA.Path {
		t.Fatalf("different presets shared a cache entry: %q", gridA.Path)
	}
	// The key includes the source size/mtime, so a changed source must
	// resolve to a new (miss) entry rather than the stale one.
	src.mu.Lock()
	src.entries["/quark/a.jpg"] = Entry{ID: "a-1", Size: 11, ModTime: time.Unix(100, 0).UTC()}
	src.mu.Unlock()
	info, err := Get(context.Background(), cfg, src, "/quark/a.jpg", "grid-128")
	if err != nil {
		t.Fatal(err)
	}
	if info.Hit {
		t.Fatalf("Get after source change = %+v, want miss on the old key", info)
	}
	// The old and new entries live in different directories.
	changed := putOnce(t, cfg, src, "/quark/a.jpg", "grid-128", "image/jpeg", "thumb-a2")
	if changed.Path == gridA.Path {
		t.Fatalf("source change reused the old cache entry: %q", changed.Path)
	}
}

func TestMetaJSONShape(t *testing.T) {
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/photo.jpg": {ID: "photo-1", Size: 123, ModTime: time.Unix(100, 0).UTC()},
	}}
	put := putOnce(t, cfg, src, "/quark/photo.jpg", "grid-128", "image/jpeg", "thumb")
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(put.Path), "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persist map[string]any
	if err := json.Unmarshal(raw, &persist); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mime", "size", "source_key", "preset", "created_at"} {
		if _, ok := persist[key]; !ok {
			t.Fatalf("meta.json missing key %q: %s", key, raw)
		}
	}
	if persist["mime"] != "image/jpeg" || persist["preset"] != "grid-128" || int64(persist["size"].(float64)) != int64(len("thumb")) {
		t.Fatalf("meta.json values = %v", persist)
	}
	if created, ok := persist["created_at"].(string); !ok || created == "" {
		t.Fatalf("meta.json created_at = %v, want non-empty timestamp", persist["created_at"])
	}
	if _, err := time.Parse(time.RFC3339Nano, persist["created_at"].(string)); err != nil {
		t.Fatalf("meta.json created_at not RFC3339: %v", err)
	}
}

func TestValidationErrors(t *testing.T) {
	ctx := context.Background()
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/photo.jpg": {ID: "photo-1", Size: 1, ModTime: time.Unix(100, 0).UTC()},
	}}
	tests := []struct {
		name   string
		preset string
		want   string
	}{
		{"empty preset", "", "core: thumbnail preset required"},
		{"blank preset", "   ", "core: thumbnail preset required"},
		{"slash preset", "grid/128", `core: invalid thumbnail preset "grid/128"`},
		{"backslash preset", "grid\\128", `core: invalid thumbnail preset "grid\\128"`},
		{"dot preset", ".", `core: invalid thumbnail preset "."`},
		{"dotdot preset", "..", `core: invalid thumbnail preset ".."`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Get(ctx, cfg, src, "/quark/photo.jpg", tt.preset); err == nil || err.Error() != tt.want {
				t.Fatalf("Get err = %v, want %q", err, tt.want)
			}
			local := filepath.Join(t.TempDir(), "thumb")
			if err := os.WriteFile(local, []byte("t"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Put(ctx, cfg, src, "/quark/photo.jpg", tt.preset, "image/jpeg", local); err == nil || err.Error() != tt.want {
				t.Fatalf("Put err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCacheUnavailableWhenDirEmpty(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{entries: map[string]Entry{
		"/quark/photo.jpg": {ID: "photo-1", Size: 1, ModTime: time.Unix(100, 0).UTC()},
	}}
	cfg := Config{}
	if _, err := Get(ctx, cfg, src, "/quark/photo.jpg", "grid-128"); err == nil || err.Error() != "core: thumbnail cache unavailable" {
		t.Fatalf("Get err = %v, want cache-unavailable", err)
	}
	local := filepath.Join(t.TempDir(), "thumb")
	if err := os.WriteFile(local, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(ctx, cfg, src, "/quark/photo.jpg", "grid-128", "image/jpeg", local); err == nil || err.Error() != "core: thumbnail cache unavailable" {
		t.Fatalf("Put err = %v, want cache-unavailable", err)
	}
}

func TestDirectorySourceRejected(t *testing.T) {
	ctx := context.Background()
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/dir": {ID: "dir-1", IsDir: true, Size: 0},
	}}
	want := "core: /quark/dir is a directory"
	if _, err := Get(ctx, cfg, src, "/quark/dir", "grid-128"); err == nil || err.Error() != want {
		t.Fatalf("Get err = %v, want %q", err, want)
	}
	local := filepath.Join(t.TempDir(), "thumb")
	if err := os.WriteFile(local, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(ctx, cfg, src, "/quark/dir", "grid-128", "image/jpeg", local); err == nil || err.Error() != want {
		t.Fatalf("Put err = %v, want %q", err, want)
	}
}

func TestSourceStatErrorPassesThrough(t *testing.T) {
	ctx := context.Background()
	cfg := defaultCfg(t)
	boom := errors.New("quark: stat boom")
	src := &fakeSource{entries: map[string]Entry{}, err: boom}
	if _, err := Get(ctx, cfg, src, "/quark/photo.jpg", "grid-128"); !errors.Is(err, boom) {
		t.Fatalf("Get err = %v, want stat error passthrough", err)
	}
	local := filepath.Join(t.TempDir(), "thumb")
	if err := os.WriteFile(local, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(ctx, cfg, src, "/quark/photo.jpg", "grid-128", "image/jpeg", local); !errors.Is(err, boom) {
		t.Fatalf("Put err = %v, want stat error passthrough", err)
	}
}

func TestPutCancellationPropagatesContextError(t *testing.T) {
	cfg := defaultCfg(t)
	// The stat fake ignores ctx so the service proceeds to the copy path,
	// which is where Put observes cancellation (the atomic writer callback
	// reads through a ctx-aware reader).
	src := &fakeSource{statIgnoresCtx: true, entries: map[string]Entry{
		"/quark/photo.jpg": {ID: "photo-1", Size: 1, ModTime: time.Unix(100, 0).UTC()},
	}}
	local := filepath.Join(t.TempDir(), "thumb")
	if err := os.WriteFile(local, []byte("thumb"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Put(ctx, cfg, src, "/quark/photo.jpg", "grid-128", "image/jpeg", local)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put err = %v, want context.Canceled", err)
	}
	if err.Error() != context.Canceled.Error() {
		t.Fatalf("Put err text = %q, want unreformatted context error", err.Error())
	}
	// A cancelled context must not have left a usable entry behind.
	if _, err := Get(context.Background(), cfg, src, "/quark/photo.jpg", "grid-128"); err != nil {
		t.Fatal(err)
	}
}

func TestGetCancellationPropagatesContextError(t *testing.T) {
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/photo.jpg": {ID: "photo-1", Size: 1, ModTime: time.Unix(100, 0).UTC()},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Get(ctx, cfg, src, "/quark/photo.jpg", "grid-128")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Get err = %v, want context.Canceled", err)
	}
}

func TestPruneKeepsNewestAndHonoursBudget(t *testing.T) {
	ctx := context.Background()
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/a.jpg": {ID: "a-1", Size: 1, ModTime: time.Unix(100, 0).UTC()},
		"/quark/b.jpg": {ID: "b-1", Size: 1, ModTime: time.Unix(200, 0).UTC()},
		"/quark/c.jpg": {ID: "c-1", Size: 1, ModTime: time.Unix(300, 0).UTC()},
	}}
	// A is the largest thumbnail, C the smallest, so removing A always
	// brings the total under the budget that fits A+B.
	a := putOnce(t, cfg, src, "/quark/a.jpg", "grid-128", "image/jpeg", strings.Repeat("A", 60))
	time.Sleep(20 * time.Millisecond)
	b := putOnce(t, cfg, src, "/quark/b.jpg", "grid-128", "image/jpeg", strings.Repeat("B", 40))
	budget := dirBytes(t, filepath.Dir(a.Path)) + dirBytes(t, filepath.Dir(b.Path))
	time.Sleep(20 * time.Millisecond)
	cfg.MaxBytes = budget
	c := putOnce(t, cfg, src, "/quark/c.jpg", "grid-128", "image/jpeg", "C")

	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Fatalf("oldest thumbnail still exists after prune, err=%v", err)
	}
	if _, err := os.Stat(b.Path); err != nil {
		t.Fatalf("second thumbnail should be kept: %v", err)
	}
	if _, err := os.Stat(c.Path); err != nil {
		t.Fatalf("latest thumbnail should be kept: %v", err)
	}
	// The entry that was just written is the keepDir: the cache must still
	// serve it.
	hit, err := Get(ctx, cfg, src, "/quark/c.jpg", "grid-128")
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Hit || hit.Path != c.Path {
		t.Fatalf("Get after prune = %+v, want hit on latest", hit)
	}
}

func TestPruneKeepsEveryEntryWhenSizeFitsBudget(t *testing.T) {
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/a.jpg": {ID: "a-1", Size: 1, ModTime: time.Unix(100, 0).UTC()},
		"/quark/b.jpg": {ID: "b-1", Size: 1, ModTime: time.Unix(200, 0).UTC()},
	}}
	a := putOnce(t, cfg, src, "/quark/a.jpg", "grid-128", "image/jpeg", "thumb-a")
	time.Sleep(20 * time.Millisecond)
	b := putOnce(t, cfg, src, "/quark/b.jpg", "grid-128", "image/jpeg", "thumb-b")
	if _, err := os.Stat(a.Path); err != nil {
		t.Fatalf("entry removed though cache fits budget: %v", err)
	}
	if _, err := os.Stat(b.Path); err != nil {
		t.Fatal(err)
	}
}

func TestPruneDisabledWhenMaxNonPositive(t *testing.T) {
	cfg := defaultCfg(t)
	cfg.MaxBytes = 0
	src := &fakeSource{entries: map[string]Entry{
		"/quark/a.jpg": {ID: "a-1", Size: 1, ModTime: time.Unix(100, 0).UTC()},
		"/quark/b.jpg": {ID: "b-1", Size: 1, ModTime: time.Unix(200, 0).UTC()},
	}}
	a := putOnce(t, cfg, src, "/quark/a.jpg", "grid-128", "image/jpeg", "thumb-a")
	time.Sleep(20 * time.Millisecond)
	_ = putOnce(t, cfg, src, "/quark/b.jpg", "grid-128", "image/jpeg", "thumb-b")
	if _, err := os.Stat(a.Path); err != nil {
		t.Fatalf("entry removed with MaxBytes <= 0: %v", err)
	}
}

func TestRepeatPutOverwritesSameEntry(t *testing.T) {
	cfg := defaultCfg(t)
	src := &fakeSource{entries: map[string]Entry{
		"/quark/photo.jpg": {ID: "photo-1", Size: 1, ModTime: time.Unix(100, 0).UTC()},
	}}
	first := putOnce(t, cfg, src, "/quark/photo.jpg", "grid-128", "image/jpeg", "v1")
	second := putOnce(t, cfg, src, "/quark/photo.jpg", "grid-128", "image/png", "v2-longer")
	if filepath.Dir(second.Path) != filepath.Dir(first.Path) {
		t.Fatalf("re-put moved to a new entry dir: %q -> %q", first.Path, second.Path)
	}
	checkThumbnailFile(t, second.Path, "v2-longer")
	// The old .jpg file must be gone; only the new png remains.
	files, err := os.ReadDir(filepath.Dir(second.Path))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name() != "thumbnail.png" && f.Name() != "meta.json" {
			t.Fatalf("stale cache file %q after re-put", f.Name())
		}
	}
	hit, err := Get(context.Background(), cfg, src, "/quark/photo.jpg", "grid-128")
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Hit || hit.Mime != "image/png" || hit.Size != int64(len("v2-longer")) {
		t.Fatalf("Get after re-put = %+v, want updated entry", hit)
	}
}

// TestConcurrentPutsAndGets exercises the race detector with distinct
// sources (each with its own cache dir) plus shared reads of one entry.
func TestConcurrentPutsAndGets(t *testing.T) {
	cfg := defaultCfg(t)
	const workers = 12
	src := &fakeSource{entries: map[string]Entry{}}
	var paths []string
	for i := 0; i < workers; i++ {
		path := fmt.Sprintf("/quark/p%02d.jpg", i)
		src.entries[path] = Entry{ID: fmt.Sprintf("p%02d", i), Size: int64(i + 1), ModTime: time.Unix(int64(100+i), 0).UTC()}
		paths = append(paths, path)
	}
	// The shared source: every worker writes a different preset but the same
	// source, so they land in different cache dirs. Read a pre-written entry
	// concurrently afterwards to exercise shared reads.
	putOnce(t, cfg, src, paths[0], "grid-128", "image/jpeg", "shared")

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	results := make([]Info, workers)
	for i, path := range paths {
		wg.Add(1)
		go func(i int, path, content string) {
			defer wg.Done()
			local := filepath.Join(t.TempDir(), fmt.Sprintf("thumb-%d", i))
			if err := os.WriteFile(local, []byte(content), 0o644); err != nil {
				errs <- err
				return
			}
			info, err := Put(context.Background(), cfg, src, path, "grid-128", "image/jpeg", local)
			if err != nil {
				errs <- err
				return
			}
			results[i] = info
		}(i, path, fmt.Sprintf("thumb-%d", i))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for i, info := range results {
		if !info.Hit {
			t.Fatalf("worker %d Put = %+v, want hit", i, info)
		}
		checkThumbnailFile(t, info.Path, fmt.Sprintf("thumb-%d", i))
	}
	// The shared entry is readable while the workers above may still be
	// pruning (prune keeps each worker's own freshly written dir).
	var readWG sync.WaitGroup
	readErrs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		readWG.Add(1)
		go func() {
			defer readWG.Done()
			info, err := Get(context.Background(), cfg, src, paths[0], "grid-128")
			if err != nil {
				readErrs <- err
				return
			}
			if !info.Hit {
				readErrs <- fmt.Errorf("Get shared = %+v, want hit", info)
			}
		}()
	}
	readWG.Wait()
	close(readErrs)
	for err := range readErrs {
		t.Fatal(err)
	}
}

func dirBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
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
	if err != nil {
		t.Fatal(err)
	}
	return total
}
