package vfs_test

import (
	"context"
	"encoding/json"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"strings"
	"testing"
)

type limitedPolicyDriver struct {
	*drive.FakeDriver
	policy drive.ReadPolicy
	limit  int
}

func (d limitedPolicyDriver) ReadPolicy() drive.ReadPolicy { return d.policy }
func (d limitedPolicyDriver) ReadLimits() drive.ReadLimits {
	return drive.ReadLimits{MaxInFlight: d.limit, Parallel: true}
}
func TestReadPolicySnapshotAndSourceIdentity(t *testing.T) {
	p := drive.DefaultReadPolicy()
	p.Bulk.Streams = 6
	d := limitedPolicyDriver{drive.NewFakeDriver(drive.FakeWithCapabilities(drive.CapabilityParallelRange)), p, 2}
	budget, streams := 8, 5
	window := int64(8 << 20)
	a, err := vfs.New(d, vfs.Options{Name: "a", StorageDir: t.TempDir(), Read: &drive.ReadOverride{MaxInFlight: &budget, BulkStreams: &streams, BulkWindowBytes: &window}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	b, err := vfs.New(d, vfs.Options{Name: "b", StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(context.Background())
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "a", FS: a}, {Name: "b", FS: b}})
	if err != nil {
		t.Fatal(err)
	}
	source := a.ReadSourceForPath("/one")
	if source == nil || source != a.ReadSourceForPath("/renamed") || source != ns.ReadSourceForPath("/a/one") || source != ns.ReadSourceForMount("a") {
		t.Fatal("source identity was lost")
	}
	if source.Policy().SourceID == b.ReadSourceForMount("b").Policy().SourceID {
		t.Fatal("different sources share a diagnostic identity")
	}
	if source == ns.ReadSourceForMount("b") || ns.ReadSourceForMount("missing") != nil || ns.ReadSourceForPath("/") != nil {
		t.Fatal("source scope leaked across mounts")
	}
	snapshot := a.DebugSnapshot().Mounts[0].ReadPolicy
	if snapshot.Effective.MaxInFlight != 2 || snapshot.Effective.Bulk.Streams != 2 || snapshot.Effective.Bulk.WindowBytes != window || snapshot.Limits.MaxInFlight != 2 || snapshot.Origins["bulk.window"] != "mount" {
		t.Fatalf("policy=%+v", snapshot)
	}
	snapshot.Origins["bulk.window"] = "mutated"
	if source.Policy().Origins["bulk.window"] != "mount" {
		t.Fatal("diagnostic snapshot mutated live policy")
	}
	data, err := json.Marshal(a.DebugSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"read_profile"`) || !strings.Contains(string(data), `"read_policy"`) {
		t.Fatalf("legacy JSON contract remains: %s", data)
	}
}
func TestInvalidCompleteReadPolicyFailsMountConstruction(t *testing.T) {
	p := drive.DefaultReadPolicy()
	p.Interactive.Seek.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: 1 << 20, Depth: 0}
	d := limitedPolicyDriver{drive.NewFakeDriver(), p, 2}
	if fs, err := vfs.New(d, vfs.Options{StorageDir: t.TempDir()}); err == nil {
		_ = fs.Close(context.Background())
		t.Fatal("invalid complete policy accepted")
	}
}
