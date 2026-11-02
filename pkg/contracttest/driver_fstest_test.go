package contracttest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

type permanentFailSnapshotter struct {
	snapshot diagnostics.DebugSnapshot
}

func (s permanentFailSnapshotter) DebugSnapshot() diagnostics.DebugSnapshot { return s.snapshot }

// TestWaitVFSSmokeIdleIgnoresPermanentFailures pins that a terminal upload
// record no longer counts as active work. Before this, every idle wait spun
// until its timeout even though no upload would ever run again.
func TestWaitVFSSmokeIdleIgnoresPermanentFailures(t *testing.T) {
	stub := permanentFailSnapshotter{snapshot: diagnostics.DebugSnapshot{
		Mounts: []diagnostics.MountSnapshot{{
			Identity: diagnostics.MountSnapshotIdentity{Name: "default"},
			Overlay: diagnostics.MountSnapshotOverlay{
				Pending: []vfstypes.PendingUpload{{
					Path:          "/stuck.txt",
					PermanentFail: true,
					LastError:     "auth expired",
				}},
			},
		}},
	}}
	started := time.Now()
	if err := waitVFSSmokeIdle(context.Background(), stub, "default", time.Second, nil); err != nil {
		t.Fatalf("waitVFSSmokeIdle: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("idle wait took %s; terminal upload was treated as active", elapsed)
	}
}

// TestRunVFSSmokeTestCleansRemoteOnCanceledContext pins issue #16: a probe
// canceled while an upload is pending must still remove its temporary remote
// directory, using a cleanup context detached from the probe context.
func TestRunVFSSmokeTestCleansRemoteOnCanceledContext(t *testing.T) {
	d := drive.NewFakeDriver()
	d.ErrPutSource = fmt.Errorf("%w: permanent", drive.ErrAuth)
	fs, err := vfs.New(d, vfs.Options{
		StorageDir:  t.TempDir(),
		UploadDelay: 10 * time.Millisecond,
		DeleteDelay: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = fs.Close(stopCtx)
	}()

	probeCtx, probeCancel := context.WithCancel(context.Background())
	defer probeCancel()
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, pending := range fs.PendingUploads() {
				if pending.PermanentFail {
					probeCancel()
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		probeCancel()
	}()

	result := RunVFSSmokeTest(probeCtx, fs, "default", 16)
	if result.Pass {
		t.Fatal("probe passed despite a canceled upload")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := d.List(context.Background(), "0")
		if err != nil {
			t.Fatalf("list remote root: %v", err)
		}
		leftover := ""
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name, "__qrypt_fs_test_") {
				leftover = entry.Name
				break
			}
		}
		if leftover == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe left remote test directory %q behind", leftover)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
