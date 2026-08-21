package contracttest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestRunVFSMountedReadTestRejectsBackendDirectoryAsMountPoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	driver := localfs.New(remote)
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(driver, vfs.Options{
		Name: "local", StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20,
		UploadDelay: time.Millisecond, DeleteDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	defer func() {
		cancel()
		_ = fs.Close(context.Background())
	}()

	result := RunVFSMountedReadTest(ctx, fs, "local", DriverTestRequest{
		Mount: "local", MountPoint: remote, Size: "64k", BlockSize: "4k",
		CacheMode: "both", Samples: 1,
	})
	if result.Pass {
		t.Fatalf("backend directory was accepted as a qrypt mount point: %+v", result)
	}
	if len(result.Measurements) != 1 {
		t.Fatalf("measurements = %d, want rejected cold measurement", len(result.Measurements))
	}
	measurement := result.Measurements[0]
	if measurement.Bytes != 64<<10 || measurement.TraversedVFS || measurement.VFSReadCalls != 0 {
		t.Fatalf("invalid rejected measurement: %+v", measurement)
	}
	if got := result.Steps[len(result.Steps)-1].Error; got != "mounted read did not traverse qrypt; verify --mount-point" {
		t.Fatalf("error = %q", got)
	}
}

func TestMountedTestPathRequiresDirectory(t *testing.T) {
	if _, err := mountedTestPath("", "/data.bin"); err == nil {
		t.Fatal("empty mount point accepted")
	}
	file := t.TempDir() + "/file"
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mountedTestPath(file, "/data.bin"); err == nil {
		t.Fatal("file mount point accepted")
	}
}
