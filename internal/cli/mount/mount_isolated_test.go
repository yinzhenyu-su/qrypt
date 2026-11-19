package mount

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
)

func TestApplyIsolatedOverrides(t *testing.T) {
	cfg := &config.Config{
		MountPoint: "/Users/someone/Qrypt",
		VolumeName: "Qrypt",
		Mounts: []config.MountConfig{
			{Name: "one", Type: "local", TestEnabled: false},
			{Name: "two", Type: "local", TestEnabled: false},
		},
		ReadCache: config.ReadCacheConfig{MaxSize: "10G"},
		Logging:   config.LoggingConfig{LogLevel: "debug", LogFile: "/tmp/qrypt.log"},
	}
	if err := applyIsolatedOverrides(cfg, "2G", "info", ""); err != nil {
		t.Fatal(err)
	}
	if !cfg.ReadOnly || cfg.Mount.ReadOnly == nil || !*cfg.Mount.ReadOnly {
		t.Fatalf("isolated mount must be read-only: read_only=%v mount.read_only=%v", cfg.ReadOnly, cfg.Mount.ReadOnly)
	}
	if cfg.MountPoint != "" || cfg.Mount.MountPoint != "" {
		t.Fatalf("isolated mount must not reuse the configured mount point: %q %q", cfg.MountPoint, cfg.Mount.MountPoint)
	}
	cfg.MountPoint = "/tmp/isolated/mnt" // the command sets this explicitly
	if cfg.ReadCache.MaxSize != "2G" {
		t.Fatalf("cache = %q, want 2G", cfg.ReadCache.MaxSize)
	}
	if cfg.Logging.LogLevel != "info" || cfg.Logging.LogFile != "" {
		t.Fatalf("logging should be quiet and file-less: %+v", cfg.Logging)
	}
	for _, m := range cfg.Mounts {
		if !m.TestEnabled {
			t.Fatalf("mount %q should accept probes in an isolated run", m.Name)
		}
	}
}
