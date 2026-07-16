package vfs_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/driver/localfs"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestVFSDebugSnapshotReportsDriverCapabilities(t *testing.T) {
	ctx := context.Background()
	drv := localfs.New(t.TempDir())
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	snapshot := fs.DebugSnapshot()
	if len(snapshot.Mounts) != 1 {
		t.Fatalf("mount count = %d, want 1", len(snapshot.Mounts))
	}
	if snapshot.Mounts[0].Identity.RootID != "0" {
		t.Fatalf("root id = %q, want default root id", snapshot.Mounts[0].Identity.RootID)
	}
	caps := map[drive.Capability]bool{}
	for _, capability := range snapshot.Mounts[0].Identity.Capabilities {
		caps[capability] = true
	}
	for _, capability := range []drive.Capability{
		drive.CapabilityWriter,
		drive.CapabilitySourceUploader,
		drive.CapabilitySpace,
		drive.CapabilityPathResolver,
	} {
		if !caps[capability] {
			t.Fatalf("debug capabilities = %+v, missing %s", snapshot.Mounts[0].Identity.Capabilities, capability)
		}
	}
}

func TestVFSDebugSnapshotUsesConfiguredName(t *testing.T) {
	ctx := context.Background()
	drv := localfs.New(t.TempDir())
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(drv, vfs.Options{Name: "cloud", CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	snapshot := fs.DebugSnapshot()
	if len(snapshot.Mounts) != 1 {
		t.Fatalf("mount count = %d, want 1", len(snapshot.Mounts))
	}
	if snapshot.Mounts[0].Identity.Name != "cloud" {
		t.Fatalf("debug mount name = %q, want configured name", snapshot.Mounts[0].Identity.Name)
	}
	filtered := fs.DebugSnapshotForMounts([]string{"cloud"})
	if len(filtered.Mounts) != 1 || filtered.Mounts[0].Identity.Name != "cloud" {
		t.Fatalf("filtered snapshot = %+v, want cloud mount", filtered.Mounts)
	}
	filtered = fs.DebugSnapshotForMounts([]string{"default"})
	if len(filtered.Mounts) != 0 {
		t.Fatalf("default should not match configured mount name: %+v", filtered.Mounts)
	}
	staging, err := fs.DebugStaging(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(staging.Mounts) != 1 || staging.Mounts[0].Mount != "cloud" {
		t.Fatalf("staging mount = %+v, want cloud", staging.Mounts)
	}
}

func TestVFSDebugReadHistoryIsBounded(t *testing.T) {
	ctx := context.Background()
	drv := localfs.New(t.TempDir())
	if err := drv.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		_, _ = fs.Read(ctx, fmt.Sprintf("/missing-%d", i), 0, 0)
	}
	reads := fs.DebugSnapshot().Mounts[0].ReadEvents()
	if len(reads) != 100 {
		t.Fatalf("read history count = %d, want 100", len(reads))
	}
	if reads[0].State != "failed" || reads[0].ErrorCategory == "" {
		t.Fatalf("bounded history missing structured failure: %+v", reads[0])
	}
}

func TestVFSDebugConsistencyPreservesZeroBytePendingSize(t *testing.T) {
	ctx := context.Background()
	drv := &countingUploadDriver{entries: map[string]drive.Entry{
		"remote-zero": {ID: "remote-zero", ParentID: "0", Name: "zero.txt", Size: 5},
	}}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Create(ctx, "/zero.txt"); err != nil {
		t.Fatal(err)
	}

	report, err := fs.DebugConsistency(ctx, "/zero.txt")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "mismatch" || report.ExpectedSize != 0 || report.RemoteSize != 5 || report.SizeMatches {
		t.Fatalf("expected zero-byte pending mismatch, got %+v", report)
	}
}

func TestVFSDebugSnapshotShowsActiveUploadProgress(t *testing.T) {
	ctx := context.Background()
	drv := newBlockingUploadDriver()
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/active.txt", []byte("active upload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/active.txt"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drv.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not start")
	}

	snapshot := fs.DebugSnapshot()
	if len(snapshot.Mounts) != 1 || len(snapshot.Mounts[0].ActiveUploads()) != 1 {
		t.Fatalf("expected one active upload, got %+v", snapshot)
	}
	upload := snapshot.Mounts[0].ActiveUploads()[0]
	if upload.Path != "/active.txt" || upload.State != string(drive.UploadPhaseCommitting) || upload.BytesTotal != int64(len("active upload")) || upload.BytesUploaded != int64(len("active upload")) {
		t.Fatalf("unexpected active upload: %+v", upload)
	}
	close(drv.release)
	waitNoPending(t, fs)
}

func TestVFSDebugStagingReportsSizeMismatch(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()

	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/mismatch.txt", []byte("expected"), 0); err != nil {
		t.Fatal(err)
	}
	pending := fs.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}
	if err := os.WriteFile(pending[0].LocalPath, []byte("bad"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := fs.DebugStaging(ctx, "/mismatch.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Mounts) != 1 || len(report.Mounts[0].Files) != 1 {
		t.Fatalf("unexpected staging report: %+v", report)
	}
	file := report.Mounts[0].Files[0]
	if file.SizeMatches {
		t.Fatalf("expected size mismatch, got %+v", file)
	}
	if file.PendingSize != int64(len("expected")) || file.StagingSize != int64(len("bad")) {
		t.Fatalf("unexpected sizes in report: %+v", file)
	}
}

func TestEncryptedDebugConsistencyReportsForeignPlainFiles(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	raw := localfs.New(remote)
	cp, err := crypt.NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	drv := crypt.NewDriver(raw, cp, crypt.DriverOptions{})
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/secret.txt", []byte("encrypted"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/secret.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)
	if err := os.WriteFile(filepath.Join(remote, "plain.txt"), []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := fs.DebugConsistency(ctx, "/secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" {
		t.Fatalf("status = %q, want ok: %+v", report.Status, report)
	}
	if len(report.ForeignEntries) != 1 {
		t.Fatalf("foreign entries = %+v, want one", report.ForeignEntries)
	}
	if report.ForeignEntries[0].RemoteName != "plain.txt" || report.ForeignEntries[0].Reason != "filename_decrypt_failed" {
		t.Fatalf("unexpected foreign entry: %+v", report.ForeignEntries[0])
	}
}

func TestPlainDebugConsistencyDoesNotReportForeignFiles(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	raw := localfs.New(remote)
	fs, err := vfs.New(raw, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	if err := os.WriteFile(filepath.Join(remote, "plain.txt"), []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := fs.DebugConsistency(ctx, "/plain.txt")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || !report.RemoteFound {
		t.Fatalf("unexpected plain consistency report: %+v", report)
	}
	if len(report.ForeignEntries) != 0 {
		t.Fatalf("plain mount reported foreign entries: %+v", report.ForeignEntries)
	}
}

func TestVFSMountHealthTracksUserOperations(t *testing.T) {
	ctx := context.Background()
	fs, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{
		CacheDir:      t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fs.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Create(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("hello"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(ctx, "/draft.txt", "/final.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(ctx, "/final.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(ctx, "/missing.txt"); err == nil {
		t.Fatal("stat of missing file should fail")
	}
	if rc, err := fs.Read(ctx, "/missing.txt", 0, 1); err == nil {
		_ = rc.Close()
		t.Fatal("read of missing file should fail")
	}

	health := singleMountHealth(t, fs)
	if health.OK {
		t.Fatalf("health should be degraded after operation failures: %+v", health)
	}
	if health.Level != drive.HealthLevelDegraded {
		t.Fatalf("level = %q, want %q", health.Level, drive.HealthLevelDegraded)
	}
	assertHealthOp(t, health, drive.HealthOpList, 1, 0)
	assertHealthOp(t, health, drive.HealthOpCreate, 1, 0)
	assertHealthOp(t, health, drive.HealthOpRename, 1, 0)
	assertHealthOp(t, health, drive.HealthOpDelete, 1, 0)
	if got := health.Ops[drive.HealthOpWrite]; got.Success < 2 || got.Errors != 0 {
		t.Fatalf("write health = %+v, want at least 2 successes and 0 errors", got)
	}
	if got := health.Ops[drive.HealthOpStat]; got.Errors != 1 || got.LastError == "" || got.LastErrorAt.IsZero() {
		t.Fatalf("stat health = %+v, want one recorded error", got)
	}
	if got := health.Ops[drive.HealthOpRead]; got.Errors != 1 || got.LastError == "" || got.LastErrorAt.IsZero() {
		t.Fatalf("read health = %+v, want one recorded error", got)
	}
}

func TestVFSMountHealthIncludesDriverMetrics(t *testing.T) {
	driver := &metricHealthDriver{
		countingReadDriver: newCountingReadDriver([]byte("data")),
		metrics: []drive.MetricEvent{{
			At:        time.Now(),
			Operation: "driver_api",
			OK:        false,
			Error:     "driver api failed",
		}},
	}
	fs, err := vfs.New(driver, vfs.Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	health := singleMountHealth(t, fs)
	if health.OK || health.Level != drive.HealthLevelDegraded {
		t.Fatalf("health = %+v, want degraded from driver metric", health)
	}
	if got := health.Ops["driver_api"]; got.Errors != 1 || got.LastError != "driver api failed" {
		t.Fatalf("driver_api health = %+v, want one driver metric error", got)
	}
}

func TestVFSRemoteDeleteUpdatesMountHealth(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "data.txt"), []byte("delete me"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		CacheDir:      t.TempDir(),
		CacheMaxBytes: 10 << 20,
		DeleteDelay:   10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := fs.Remove(ctx, "/data.txt"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		_, err := os.Stat(filepath.Join(remote, "data.txt"))
		return os.IsNotExist(err)
	})

	health := singleMountHealth(t, fs)
	if got := health.Ops[drive.HealthOpDelete]; got.Success < 2 || got.Errors != 0 {
		t.Fatalf("delete health = %+v, want queued and remote delete successes", got)
	}
}
