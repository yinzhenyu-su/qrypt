package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestRunDriverCRUDTestReportsContractMatrixAndCleanup(t *testing.T) {
	driver := newCRUDMemoryDriver()
	result := RunDriverCRUDTest(context.Background(), "mem", driver)
	if !result.Pass {
		t.Fatalf("crud test pass = false, steps=%#v cleanup=%#v residual=%#v", result.Steps, result.Cleanup, result.Residual)
	}
	if result.OpID == "" || result.RetryCommand == "" || result.Duration == "" {
		t.Fatalf("result missing diagnostic metadata: %+v", result)
	}
	if result.DurationMS <= 0 {
		t.Fatalf("result duration_ms = %d, want positive", result.DurationMS)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !bytes.Contains(body, []byte(`"duration_ms"`)) || !bytes.Contains(body, []byte(`"elapsed_ms"`)) {
		t.Fatalf("result JSON missing machine-comparable duration fields: %s", body)
	}
	if len(result.Created) < 8 {
		t.Fatalf("created artifacts = %d, want test dir, nested dir, and matrix files: %#v", len(result.Created), result.Created)
	}
	if len(result.Cleanup) == 0 {
		t.Fatalf("cleanup report is empty")
	}
	if len(result.Residual) != 0 {
		t.Fatalf("unexpected residual artifacts: %#v", result.Residual)
	}
	if len(result.ResidualTimeline) == 0 {
		t.Fatalf("residual timeline is empty")
	}
	if len(result.Metrics) == 0 {
		t.Fatalf("metrics is empty")
	}
	seenOps := map[string]bool{}
	seenNames := map[string]bool{}
	for _, step := range result.Steps {
		if step.OpID != result.OpID || step.Mount != "mem" || step.Driver != "memory" {
			t.Fatalf("step missing unified metadata: %+v", step)
		}
		if step.Input == nil && (step.Operation == "put" || step.Operation == "read" || step.Operation == "verify_put_list") {
			t.Fatalf("step %s/%s missing input: %+v", step.Operation, step.Name, step)
		}
		if step.Expected == nil && (step.Operation == "put" || step.Operation == "read" || step.Operation == "verify_put_list") {
			t.Fatalf("step %s/%s missing expected values: %+v", step.Operation, step.Name, step)
		}
		if step.Actual == nil && (step.Operation == "put" || step.Operation == "read" || step.Operation == "verify_put_list") {
			t.Fatalf("step %s/%s missing actual values: %+v", step.Operation, step.Name, step)
		}
		seenOps[step.Operation] = true
		seenNames[step.Name] = true
	}
	for _, op := range []string{"mkdir", "verify_mkdir_list", "mkdir_nested", "put", "verify_put_list", "read", "rename", "verify_rename_list", "verify_cleanup_list"} {
		if !seenOps[op] {
			t.Fatalf("missing operation %q in steps: %#v", op, result.Steps)
		}
	}
	for _, name := range []string{"empty.bin", "one-byte.bin", "space name.txt", "unicode-中文.txt", "nested.txt"} {
		if !seenNames[name] {
			t.Fatalf("missing matrix case %q in steps: %#v", name, result.Steps)
		}
	}
}

type crudMemoryDriver struct {
	drive.UnsupportedOperations
	entries map[string]drive.Entry
	data    map[string][]byte
	child   map[string][]string
	next    int
	listErr error
}

func newCRUDMemoryDriver() *crudMemoryDriver {
	d := &crudMemoryDriver{
		entries: map[string]drive.Entry{},
		data:    map[string][]byte{},
		child:   map[string][]string{},
	}
	d.entries["root"] = drive.Entry{ID: "root", Name: "", IsDir: true}
	return d
}

func (d *crudMemoryDriver) Init(context.Context) error { return nil }
func (d *crudMemoryDriver) Drop(context.Context) error { return nil }

func (d *crudMemoryDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	if d.listErr != nil {
		return nil, d.listErr
	}
	if parentID == "" {
		parentID = "root"
	}
	if _, ok := d.entries[parentID]; !ok {
		return nil, fmt.Errorf("parent %q not found", parentID)
	}
	ids := append([]string(nil), d.child[parentID]...)
	sort.Strings(ids)
	entries := make([]drive.Entry, 0, len(ids))
	for _, id := range ids {
		if entry, ok := d.entries[id]; ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (d *crudMemoryDriver) Read(_ context.Context, entry drive.Entry, _, _ int64) (io.ReadCloser, error) {
	data, ok := d.data[entry.ID]
	if !ok {
		return nil, fmt.Errorf("file %q not found", entry.ID)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (d *crudMemoryDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}

func (d *crudMemoryDriver) Mkdir(_ context.Context, parentID, name string) (drive.Entry, error) {
	if parentID == "" {
		parentID = "root"
	}
	if _, ok := d.entries[parentID]; !ok {
		return drive.Entry{}, fmt.Errorf("parent %q not found", parentID)
	}
	d.next++
	id := fmt.Sprintf("dir-%d", d.next)
	entry := drive.Entry{ID: id, ParentID: parentID, Name: name, IsDir: true, ModTime: time.Now()}
	d.entries[id] = entry
	d.child[parentID] = append(d.child[parentID], id)
	return entry, nil
}

func (d *crudMemoryDriver) Move(_ context.Context, entry drive.Entry, dstParentID string) error {
	if _, ok := d.entries[dstParentID]; !ok {
		return fmt.Errorf("parent %q not found", dstParentID)
	}
	current, ok := d.entries[entry.ID]
	if !ok {
		return fmt.Errorf("entry %q not found", entry.ID)
	}
	d.removeChild(current.ParentID, entry.ID)
	current.ParentID = dstParentID
	d.entries[entry.ID] = current
	d.child[dstParentID] = append(d.child[dstParentID], entry.ID)
	return nil
}

func (d *crudMemoryDriver) Rename(_ context.Context, entry drive.Entry, newName string) error {
	current, ok := d.entries[entry.ID]
	if !ok {
		return fmt.Errorf("entry %q not found", entry.ID)
	}
	current.Name = newName
	d.entries[entry.ID] = current
	return nil
}

func (d *crudMemoryDriver) Remove(_ context.Context, entry drive.Entry) error {
	current, ok := d.entries[entry.ID]
	if !ok {
		return fmt.Errorf("entry %q not found", entry.ID)
	}
	if current.IsDir && len(d.child[entry.ID]) > 0 {
		return fmt.Errorf("directory %q is not empty", entry.ID)
	}
	d.removeChild(current.ParentID, entry.ID)
	delete(d.entries, entry.ID)
	delete(d.data, entry.ID)
	return nil
}

func (d *crudMemoryDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	if _, ok := d.entries[req.ParentID]; !ok {
		return drive.Entry{}, fmt.Errorf("parent %q not found", req.ParentID)
	}
	rc, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return drive.Entry{}, err
	}
	d.next++
	id := fmt.Sprintf("file-%d", d.next)
	entry := drive.Entry{ID: id, ParentID: req.ParentID, Name: req.Name, Size: int64(len(data)), ModTime: time.Now()}
	d.entries[id] = entry
	d.data[id] = data
	d.child[req.ParentID] = append(d.child[req.ParentID], id)
	return entry, nil
}

func (d *crudMemoryDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{Driver: "memory"}, nil
}

func (d *crudMemoryDriver) Capabilities() []drive.Capability {
	return []drive.Capability{
		drive.CapabilityPathResolver,
		drive.CapabilitySourceUploader,
		drive.CapabilityWriter,
	}
}

func (d *crudMemoryDriver) metricEvents(_ context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return []drive.MetricEvent{{
		At:        since.Add(time.Millisecond),
		OpID:      "crud-test-op",
		Step:      "put",
		Layer:     "driver.http",
		Operation: "memory",
	}}, nil
}

func (d *crudMemoryDriver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	metrics, err := d.metricEvents(ctx, since)
	if err != nil {
		return nil, err
	}
	return drive.NormalizeMetricEvents("memory", metrics), nil
}

func (d *crudMemoryDriver) ResolvePath(_ context.Context, path string) (string, error) {
	if path == "/" {
		return "root", nil
	}
	return "", fmt.Errorf("path %q not found", path)
}

func (d *crudMemoryDriver) removeChild(parentID, id string) {
	children := d.child[parentID]
	for i, child := range children {
		if child == id {
			d.child[parentID] = append(children[:i], children[i+1:]...)
			return
		}
	}
}
