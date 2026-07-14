package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func testCommand() *cobra.Command {
	return &cobra.Command{Use: "test"}
}

var (
	builderRootDriverMu   sync.Mutex
	builderRootTestDriver *builderRootDriver
)

func init() {
	drive.Register("cli-rootid-test", func(params drive.Params) (drive.Driver, error) {
		d := &builderRootDriver{rootID: params["root_id"]}
		builderRootDriverMu.Lock()
		builderRootTestDriver = d
		builderRootDriverMu.Unlock()
		return d, nil
	},
		drive.ParamDef{Name: "root_id", Type: "string", Required: true},
	)
}

type builderRootDriver struct {
	drive.UnsupportedOperations
	rootID    string
	putParent string
}

func (d *builderRootDriver) Init(context.Context) error { return nil }

func (d *builderRootDriver) Drop(context.Context) error { return nil }

func (d *builderRootDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	if parentID != d.rootID {
		return nil, fmt.Errorf("unexpected parent id %q", parentID)
	}
	return nil, nil
}

func (d *builderRootDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, fmt.Errorf("read not implemented")
}

func (d *builderRootDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}

func (d *builderRootDriver) ResolvePath(_ context.Context, path string) (string, error) {
	if path != "/" {
		return "", fmt.Errorf("unexpected path %q", path)
	}
	return d.rootID, nil
}

func (d *builderRootDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	body, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	defer body.Close()
	if _, err := io.Copy(io.Discard, body); err != nil {
		return drive.Entry{}, err
	}
	d.putParent = req.ParentID
	return drive.Entry{
		ID:       d.rootID + "/" + req.Name,
		ParentID: req.ParentID,
		Name:     req.Name,
		Size:     req.Source.Size(),
	}, nil
}

func (d *builderRootDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityPathResolver, drive.CapabilitySourceUploader}
}

func (d *builderRootDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{Driver: "cli-rootid-test", Health: drive.HealthLevelOK}, nil
}

func (d *builderRootDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

var _ drive.Driver = (*builderRootDriver)(nil)

func TestBuildFileSystemCreatesNamespaceFromMountConfig(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remoteA := filepath.Join(tmp, "remote-a")
	remoteB := filepath.Join(tmp, "remote-b")
	if err := os.MkdirAll(remoteA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteB, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remoteA+`"

[[mounts]]
name = "quark2"
type = "localfs"
[mounts.params]
root_path = "`+remoteB+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fs, cleanup, err := buildFileSystem(ctx, configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	entries, err := fs.List(ctx, "/")
	if err != nil {

		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "quark" || entries[1].Name != "quark2" {
		t.Fatalf("unexpected namespace entries: %+v", entries)
	}

	if _, err := fs.WriteAt(ctx, "/quark2/test.txt", []byte("two"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/quark2/test.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)
	data, err := os.ReadFile(filepath.Join(remoteB, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "two" {
		t.Fatalf("unexpected remote data: %q", data)
	}
}

func TestBuildFileSystemSelectsSingleMount(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remoteA := filepath.Join(tmp, "remote-a")
	remoteB := filepath.Join(tmp, "remote-b")
	if err := os.MkdirAll(remoteA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteB, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"
cache_dir = "`+filepath.Join(tmp, "cache")+`"

[defaults.cache]
upload_delay = "10ms"

[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remoteA+`"

[[mounts]]
name = "quark-test"
type = "localfs"
[mounts.params]
root_path = "`+remoteB+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	fs, cleanup, err := buildFileSystemFromConfigMount(ctx, cfg, "quark-test")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("single mount root entries = %+v, want empty remote root", entries)
	}

	if _, err := fs.WriteAt(ctx, "/test.txt", []byte("selected"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/test.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)
	data, err := os.ReadFile(filepath.Join(remoteB, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "selected" {
		t.Fatalf("unexpected selected remote data: %q", data)
	}
	if _, err := os.Stat(filepath.Join(remoteA, "test.txt")); !os.IsNotExist(err) {
		t.Fatalf("unselected remote should not receive file, stat err = %v", err)
	}
}

func TestBuildFileSystemUsesResolvedRootID(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := &config.Config{
		CacheDir: filepath.Join(tmp, "cache"),
		Defaults: config.Defaults{Cache: config.CacheConfig{
			UploadDelay: "10ms",
		}},
		Mounts: []config.MountConfig{{
			Name: "rooted",
			Type: "cli-rootid-test",
			Params: config.ParamMap{
				"root_id": "resolved-root",
			},
		}},
	}
	fs, cleanup, err := buildFileSystemFromConfigMount(ctx, cfg, "rooted")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/test.txt", []byte("root"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/test.txt"); err != nil {
		t.Fatal(err)
	}
	waitPendingEmpty(t, fs)

	builderRootDriverMu.Lock()
	driver := builderRootTestDriver
	builderRootDriverMu.Unlock()
	if driver == nil {
		t.Fatal("test driver was not constructed")
	}
	if driver.putParent != "resolved-root" {
		t.Fatalf("upload parent id = %q, want resolved root id", driver.putParent)
	}
}

func TestBuildFileSystemSelectsMissingMount(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
mount_point = "`+filepath.Join(tmp, "mnt")+`"

[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "`+remote+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := buildFileSystemFromConfigMount(ctx, cfg, "missing")
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), `mount "missing" not found`) {
		t.Fatalf("error = %v, want missing mount error", err)
	}
}
