package vfs_test

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestReadProfileOverrideReachesVFSAndSnapshot(t *testing.T) {
	driver := drive.NewFakeDriver(drive.FakeWithCapabilities(drive.CapabilityParallelRange))
	override := drive.ReadProfile{BulkStreams: 4, BulkWindowChunks: 16}
	fs, err := vfs.New(driver, vfs.Options{
		StorageDir:  t.TempDir(),
		ReadProfile: &override,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := fs.DebugSnapshot()
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
	if got := fs.ReadProfileForPath("/file"); got == nil || got.BulkStreams != 4 {
		t.Fatalf("ReadProfileForPath = %+v, want 4 streams", got)
	}
}

func TestReadProfileWithoutOverrideKeepsDriverSource(t *testing.T) {
	driver := drive.NewFakeDriver()
	fs, err := vfs.New(driver, vfs.Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	profile := fs.DebugSnapshot().Mounts[0].ReadProfile
	if profile.Streams != 1 {
		t.Fatalf("streams = %d, want 1 without parallel capability", profile.Streams)
	}
	if profile.Source != drive.ReadProfileSourceDriver {
		t.Fatalf("source = %q, want %q", profile.Source, drive.ReadProfileSourceDriver)
	}
	if got := fs.ReadProfileForPath("/file"); got != nil {
		t.Fatalf("ReadProfileForPath = %+v, want nil", got)
	}
}

func TestNamespaceReadProfileForPathAndMount(t *testing.T) {
	driver := drive.NewFakeDriver(drive.FakeWithCapabilities(drive.CapabilityParallelRange))
	override := drive.ReadProfile{BulkStreams: 4, BulkWindowChunks: 16}
	overridden, err := vfs.New(driver, vfs.Options{
		Name:        "overridden",
		StorageDir:  t.TempDir(),
		ReadProfile: &override,
	})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := vfs.New(drive.NewFakeDriver(), vfs.Options{
		Name:       "plain",
		StorageDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{
		{Name: "overridden", FS: overridden},
		{Name: "plain", FS: plain},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ns.ReadProfileForPath("/overridden/file"); got == nil || got.BulkStreams != 4 {
		t.Fatalf("ReadProfileForPath(overridden) = %+v", got)
	}
	if got := ns.ReadProfileForPath("/plain/file"); got != nil {
		t.Fatalf("ReadProfileForPath(plain) = %+v, want nil", got)
	}
	if got := ns.ReadProfileForMount("overridden"); got == nil || got.BulkStreams != 4 {
		t.Fatalf("ReadProfileForMount(overridden) = %+v", got)
	}
	if got := ns.ReadProfileForMount("plain"); got != nil {
		t.Fatalf("ReadProfileForMount(plain) = %+v, want nil", got)
	}
}

type limitedProfileFake struct {
	*drive.FakeDriver
	max int
}

func (d limitedProfileFake) MaxParallelReads() int { return d.max }

func TestReadProfileSnapshotReportsLimiterClamp(t *testing.T) {
	driver := limitedProfileFake{
		FakeDriver: drive.NewFakeDriver(drive.FakeWithCapabilities(drive.CapabilityParallelRange)),
		max:        4,
	}
	override := drive.ReadProfile{BulkStreams: 8}
	fs, err := vfs.New(driver, vfs.Options{
		StorageDir:  t.TempDir(),
		ReadProfile: &override,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile := fs.DebugSnapshot().Mounts[0].ReadProfile
	if profile.Streams != 4 {
		t.Fatalf("streams = %d, want limiter 4", profile.Streams)
	}
	if profile.Source != drive.ReadProfileSourceDriverLimiter || !profile.Clamped {
		t.Fatalf("source=%q clamped=%v, want %q/true", profile.Source, profile.Clamped, drive.ReadProfileSourceDriverLimiter)
	}
}
