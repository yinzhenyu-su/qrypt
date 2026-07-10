package drive

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type instantUploadTestDriver struct {
	rootID        string
	counter       int64
	uploads       int
	mkdirParentID string
	reportCounter bool
}

func (d *instantUploadTestDriver) Init(context.Context) error { return nil }
func (d *instantUploadTestDriver) Drop(context.Context) error { return nil }
func (d *instantUploadTestDriver) List(context.Context, string) ([]Entry, error) {
	return nil, fmt.Errorf("list should not be needed")
}
func (d *instantUploadTestDriver) Read(context.Context, Entry, int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (d *instantUploadTestDriver) Mkdir(ctx context.Context, parentID, name string) (Entry, error) {
	d.mkdirParentID = parentID
	return Entry{ID: "dir", ParentID: parentID, Name: name, IsDir: true, ModTime: time.Now()}, nil
}
func (d *instantUploadTestDriver) Move(context.Context, Entry, string) error { return nil }
func (d *instantUploadTestDriver) Rename(context.Context, Entry, string) error {
	return nil
}
func (d *instantUploadTestDriver) Remove(context.Context, Entry) error { return nil }
func (d *instantUploadTestDriver) PutSource(ctx context.Context, req UploadRequest) (Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	d.uploads++
	if d.uploads == 2 {
		d.counter++
	}
	return Entry{ID: name, ParentID: parentID, Name: name, Size: source.Size()}, nil
}
func (d *instantUploadTestDriver) DebugSnapshot(context.Context) (DebugSnapshot, error) {
	snapshot := DebugSnapshot{Driver: "instant-upload-test", Health: HealthLevelOK, GeneratedAt: time.Now()}
	if d.reportCounter {
		snapshot.Extra = map[string]any{DebugExtraInstantUploadCount: d.counter}
	}
	return snapshot, nil
}
func (d *instantUploadTestDriver) ResolvePath(context.Context, string) (string, error) {
	return d.rootID, nil
}

func TestRunInstantUploadTestUsesPathResolverRoot(t *testing.T) {
	drv := &instantUploadTestDriver{rootID: "-11", reportCounter: true}
	result := RunInstantUploadTest(context.Background(), "test", drv)
	if !result.Pass {
		t.Fatalf("instant upload test failed: %+v", result.Steps)
	}
	if drv.mkdirParentID != "-11" {
		t.Fatalf("mkdir parent = %q, want resolver root", drv.mkdirParentID)
	}
}

func TestRunInstantUploadTestFailsWhenDebugCounterMissing(t *testing.T) {
	drv := &instantUploadTestDriver{rootID: "root"}
	result := RunInstantUploadTest(context.Background(), "test", drv)
	if result.Pass {
		t.Fatalf("instant upload test passed without counter: %+v", result.Steps)
	}
	last := result.Steps[len(result.Steps)-2]
	if last.Operation != "verify_instant" || last.OK || !strings.Contains(last.Error, "counter not reported") {
		t.Fatalf("verify step = %+v, want missing counter failure", last)
	}
}
