package core

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func TestBuildMountPassesReadProfileOverride(t *testing.T) {
	root := t.TempDir()
	layout := RuntimeLayout{
		RootDir:      root,
		ReadCacheDir: filepath.Join(root, "read"),
		UploadDir:    filepath.Join(root, "upload"),
		StateDir:     filepath.Join(root, "state"),
		DriverDir:    filepath.Join(root, "state", "driver"),
	}
	cfg := &config.Config{
		Mounts: []config.MountConfig{{
			Name:        "local",
			Type:        "localfs",
			Params:      config.ParamMap{"root_path": t.TempDir()},
			ReadProfile: &config.ReadProfileConfig{Streams: 4, WindowChunks: 16},
		}},
	}
	result := buildMount(context.Background(), cfg, layout, nil, nil, cfg.Mounts[0])
	if result.err != nil {
		t.Fatal(result.err)
	}
	t.Cleanup(func() { closeMounts([]vfs.Mount{result.mount}) })

	snapshotter, ok := result.mount.FS.(interface {
		DebugSnapshot() diagnostics.DebugSnapshot
	})
	if !ok {
		t.Fatalf("mount filesystem %T has no debug snapshot", result.mount.FS)
	}
	snapshot := snapshotter.DebugSnapshot()
	if len(snapshot.Mounts) != 1 {
		t.Fatalf("mounts = %d, want 1", len(snapshot.Mounts))
	}
	profile := snapshot.Mounts[0].ReadProfile
	if profile.Streams != 4 || profile.BulkWindowChunks != 16 {
		t.Fatalf("read_profile = %+v, want 4/16", profile)
	}
	if profile.Source != drive.ReadProfileSourceMountOverride {
		t.Fatalf("source = %q, want %q", profile.Source, drive.ReadProfileSourceMountOverride)
	}
}

func TestBuildMountWithoutOverrideKeepsDriverProfile(t *testing.T) {
	root := t.TempDir()
	layout := RuntimeLayout{
		RootDir:      root,
		ReadCacheDir: filepath.Join(root, "read"),
		UploadDir:    filepath.Join(root, "upload"),
		StateDir:     filepath.Join(root, "state"),
		DriverDir:    filepath.Join(root, "state", "driver"),
	}
	cfg := &config.Config{
		Mounts: []config.MountConfig{{
			Name:   "local",
			Type:   "localfs",
			Params: config.ParamMap{"root_path": t.TempDir()},
		}},
	}
	result := buildMount(context.Background(), cfg, layout, nil, nil, cfg.Mounts[0])
	if result.err != nil {
		t.Fatal(result.err)
	}
	t.Cleanup(func() { closeMounts([]vfs.Mount{result.mount}) })

	snapshotter, ok := result.mount.FS.(interface {
		DebugSnapshot() diagnostics.DebugSnapshot
	})
	if !ok {
		t.Fatalf("mount filesystem %T has no debug snapshot", result.mount.FS)
	}
	profile := snapshotter.DebugSnapshot().Mounts[0].ReadProfile
	if profile.Source != drive.ReadProfileSourceDriver {
		t.Fatalf("source = %q, want %q", profile.Source, drive.ReadProfileSourceDriver)
	}
}

func TestShareRunEffectiveReadProfileUsesMountOverride(t *testing.T) {
	driver := drive.NewFakeDriver(drive.FakeWithCapabilities(drive.CapabilityParallelRange))
	override := drive.ReadProfile{BulkStreams: 4, BulkWindowChunks: 16}
	fs, err := vfs.New(driver, vfs.Options{
		Name:        "share",
		StorageDir:  t.TempDir(),
		ReadProfile: &override,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &shareRun{
		core:   &Core{fs: fs},
		detail: shareDetail{Mount: "share"},
		driver: driver,
	}
	got := run.effectiveReadProfile()
	if got.BulkStreams != 4 || got.BulkWindowChunks != 16 {
		t.Fatalf("share profile = %+v, want 4/16", got)
	}
}
