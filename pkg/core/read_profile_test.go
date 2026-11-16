package core

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"path/filepath"
	"testing"
)

func TestBuildMountReadPolicyAndShareUseOneSource(t *testing.T) {
	root := t.TempDir()
	window := "8MiB"
	streams := 4
	layout := RuntimeLayout{RootDir: root, ReadCacheDir: filepath.Join(root, "read"), UploadDir: filepath.Join(root, "upload"), StateDir: filepath.Join(root, "state"), DriverDir: filepath.Join(root, "state", "driver")}
	cfg := &config.Config{Mounts: []config.MountConfig{{Name: "local", Type: "localfs", Params: config.ParamMap{"root_path": t.TempDir()}, Read: &config.ReadConfig{Bulk: &config.BulkReadConfig{Streams: &streams, Window: &window}}}}}
	result := buildMount(context.Background(), cfg, layout, nil, nil, cfg.Mounts[0])
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer closeMounts([]vfs.Mount{result.mount})
	provider, ok := result.mount.FS.(drive.MountReadSourceProvider)
	if !ok {
		t.Fatalf("missing source capability: %T", result.mount.FS)
	}
	source := provider.ReadSourceForMount("local")
	if source == nil {
		t.Fatal("source absent")
	}
	p := source.Policy()
	if p.Effective.Bulk.WindowBytes != 8<<20 || p.Origins["bulk.window"] != "mount" {
		t.Fatalf("policy=%+v", p)
	}
	run := &shareRun{core: &Core{fs: result.mount.FS.(BuiltFileSystem)}, detail: shareDetail{Mount: "local"}}
	got, err := run.readSource()
	if err != nil || got != source {
		t.Fatalf("share source=%p mounted=%p error=%v", got, source, err)
	}
}
