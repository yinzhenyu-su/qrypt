package drive

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type rapidUploadTestDriver struct {
	rootID        string
	counter       int64
	uploads       int
	mkdirParentID string
	reportCounter bool
}

func (d *rapidUploadTestDriver) Init(context.Context) error { return nil }
func (d *rapidUploadTestDriver) Drop(context.Context) error { return nil }
func (d *rapidUploadTestDriver) List(context.Context, string) ([]Entry, error) {
	return nil, fmt.Errorf("list should not be needed")
}
func (d *rapidUploadTestDriver) Read(context.Context, Entry, int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (d *rapidUploadTestDriver) Mkdir(ctx context.Context, parentID, name string) (Entry, error) {
	d.mkdirParentID = parentID
	return Entry{ID: "dir", ParentID: parentID, Name: name, IsDir: true, ModTime: time.Now()}, nil
}
func (d *rapidUploadTestDriver) Move(context.Context, Entry, string) error { return nil }
func (d *rapidUploadTestDriver) Rename(context.Context, Entry, string) error {
	return nil
}
func (d *rapidUploadTestDriver) Remove(context.Context, Entry) error { return nil }
func (d *rapidUploadTestDriver) PutSource(ctx context.Context, req UploadRequest) (Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	d.uploads++
	if d.uploads == 2 {
		d.counter++
	}
	return Entry{ID: name, ParentID: parentID, Name: name, Size: source.Size()}, nil
}
func (d *rapidUploadTestDriver) DebugSnapshot(context.Context) (DebugSnapshot, error) {
	snapshot := DebugSnapshot{Driver: "rapid-test", Health: HealthLevelOK, GeneratedAt: time.Now()}
	if d.reportCounter {
		snapshot.Extra = map[string]any{"instant_upload_count": d.counter}
	}
	return snapshot, nil
}
func (d *rapidUploadTestDriver) ResolvePath(context.Context, string) (string, error) {
	return d.rootID, nil
}

func TestRunRapidUploadTestUsesPathResolverRoot(t *testing.T) {
	drv := &rapidUploadTestDriver{rootID: "-11", reportCounter: true}
	result := RunRapidUploadTest(context.Background(), "test", drv)
	if !result.Pass {
		t.Fatalf("rapid upload test failed: %+v", result.Steps)
	}
	if drv.mkdirParentID != "-11" {
		t.Fatalf("mkdir parent = %q, want resolver root", drv.mkdirParentID)
	}
}

func TestRunRapidUploadTestFailsWhenDebugCounterMissing(t *testing.T) {
	drv := &rapidUploadTestDriver{rootID: "root"}
	result := RunRapidUploadTest(context.Background(), "test", drv)
	if result.Pass {
		t.Fatalf("rapid upload test passed without counter: %+v", result.Steps)
	}
	last := result.Steps[len(result.Steps)-2]
	if last.Operation != "verify_rapid" || last.OK || !strings.Contains(last.Error, "counter not reported") {
		t.Fatalf("verify step = %+v, want missing counter failure", last)
	}
}
