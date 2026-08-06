package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// cacheEnabled reports the read-cache enabled flag from the core debug
// snapshot for the first mount.
func cacheEnabled(t *testing.T, ctx context.Context, c *Core) bool {
	t.Helper()
	raw, err := c.DebugSnapshotJSON(ctx)
	if err != nil {
		t.Fatalf("DebugSnapshotJSON: %v", err)
	}
	var snap struct {
		Mounts []struct {
			Cache struct {
				Enabled bool `json:"enabled"`
			} `json:"cache"`
		} `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if len(snap.Mounts) == 0 {
		t.Fatal("snapshot has no mounts")
	}
	return snap.Mounts[0].Cache.Enabled
}

// TestReadCacheMaxSizeZeroDisablesCache: an explicit read_cache.max_size =
// "0" must disable the mount's read cache, while an unset max_size keeps the
// default (enabled). The store short-circuits writes and reports misses.
func TestReadCacheMaxSizeZeroDisablesCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	newCore := func(t *testing.T, maxSizeLine string) *Core {
		t.Helper()
		tmp := t.TempDir()
		remote := filepath.Join(tmp, "remote")
		if err := os.MkdirAll(remote, 0o755); err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(tmp, "qrypt.toml")
		cfg := `
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "` + remote + `"
`
		if maxSizeLine != "" {
			cfg += "[mounts.read_cache]\nmax_size = \"" + maxSizeLine + "\"\n"
		}
		if err := os.WriteFile(configPath, []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Open(ctx, Options{ConfigPath: configPath, Runtime: testRuntimeLayout(tmp)})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	t.Run("explicit zero disables", func(t *testing.T) {
		c := newCore(t, "0")
		defer c.Close(ctx)
		if cacheEnabled(t, ctx, c) {
			t.Fatal("read_cache.max_size=0 did not disable the read cache")
		}
	})
	t.Run("unset keeps default", func(t *testing.T) {
		c := newCore(t, "")
		defer c.Close(ctx)
		if !cacheEnabled(t, ctx, c) {
			t.Fatal("unset read_cache.max_size did not keep the default enabled cache")
		}
	})
	t.Run("explicit size keeps enabled", func(t *testing.T) {
		c := newCore(t, "1M")
		defer c.Close(ctx)
		if !cacheEnabled(t, ctx, c) {
			t.Fatal("read_cache.max_size=1M did not keep the cache enabled")
		}
	})
}
