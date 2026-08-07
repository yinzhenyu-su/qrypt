package contracttest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestRunDriverMultipartTest(t *testing.T) {
	driver := newCRUDMemoryDriver()
	const size = 1 << 20 // 1 MiB; memory driver is chunk-agnostic, keeps CI fast
	result := RunDriverMultipartTest(context.Background(), "mem", driver, size)
	if !result.Pass {
		t.Fatalf("multipart test pass = false, steps=%#v residual=%#v", result.Steps, result.Residual)
	}
	if result.OpID == "" || result.RetryCommand == "" || result.Duration == "" {
		t.Fatalf("result missing diagnostic metadata: %+v", result)
	}
	if len(result.Created) != 2 { // test dir + large file
		t.Fatalf("created artifacts = %d, want 2: %#v", len(result.Created), result.Created)
	}
	if len(result.Residual) != 0 {
		t.Fatalf("unexpected residual artifacts: %#v", result.Residual)
	}
	if len(result.ResidualTimeline) == 0 {
		t.Fatalf("residual timeline is empty")
	}
	seen := map[string]bool{}
	for _, step := range result.Steps {
		seen[step.Operation] = true
		if step.OpID != result.OpID || step.Mount != "mem" || step.Driver != "memory" {
			t.Fatalf("step missing unified metadata: %+v", step)
		}
		if step.Operation == "read" && step.Actual["content_match"] != true {
			t.Fatalf("multipart readback content_match = %v: %+v", step.Actual["content_match"], step)
		}
	}
	for _, op := range []string{"mkdir", "put", "verify_put_list", "read", "verify_cleanup_list"} {
		if !seen[op] {
			t.Fatalf("missing step %q, got %v", op, seen)
		}
	}
	if body, err := json.Marshal(result); err != nil {
		t.Fatalf("marshal result: %v", err)
	} else if len(body) == 0 {
		t.Fatalf("empty result JSON")
	}
}

func TestRunDriverMultipartTestUnsupported(t *testing.T) {
	// A driver without SourceUploader must fail fast with a coded step.
	d := &noUploaderDriver{crudMemoryDriver: *newCRUDMemoryDriver()}
	result := RunDriverMultipartTest(context.Background(), "mem", d, 1<<10)
	if result.Pass {
		t.Fatalf("multipart test passed for driver without SourceUploader")
	}
	if len(result.Steps) != 1 || result.Steps[0].ErrorCategory != "unsupported" {
		t.Fatalf("expected single unsupported step, got %#v", result.Steps)
	}
}

type noUploaderDriver struct {
	crudMemoryDriver
}

func (d *noUploaderDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityWriter}
}

func TestFixtureLifecycle(t *testing.T) {
	d := newCRUDMemoryDriver()
	ctx := context.Background()

	fx, err := NewFixture(ctx, d, "unit")
	if err != nil {
		t.Fatalf("NewFixture: %v", err)
	}
	if fx.Name() == "" || fx.RootID() == "" {
		t.Fatalf("fixture missing identity: name=%q root=%q", fx.Name(), fx.RootID())
	}

	// The test dir must be listable immediately (memory driver is synchronous).
	entry, err := fx.VerifyList(ctx, fx.RootID(), fx.Name(), true)
	if err != nil {
		t.Fatalf("VerifyList want=true: %v", err)
	}
	if entry.ID != fx.TestDir.ID {
		t.Fatalf("VerifyList returned wrong entry: %+v", entry)
	}
	// want=true for a missing name must fail, want=false must pass.
	if _, err := fx.VerifyList(ctx, fx.RootID(), "no-such-name", true); err == nil {
		t.Fatalf("VerifyList want=true succeeded for missing name")
	}
	if _, err := fx.VerifyList(ctx, fx.RootID(), "no-such-name", false); err != nil {
		t.Fatalf("VerifyList want=false failed: %v", err)
	}

	// Remove and confirm the root scan comes back empty.
	item, ok := fx.Remove(ctx, fx.TestDir, "test_dir")
	if !ok {
		t.Fatalf("Remove failed: %+v", item)
	}
	if item.Operation != "remove" || item.Role != "test_dir" {
		t.Fatalf("cleanup result metadata wrong: %+v", item)
	}
	residual, timeline, err := fx.ScanResidual(ctx)
	if err != nil {
		t.Fatalf("ScanResidual: %v", err)
	}
	if len(residual) != 0 {
		t.Fatalf("residual after cleanup: %#v", residual)
	}
	if len(timeline) == 0 || timeline[0].Attempt != 1 {
		t.Fatalf("unexpected timeline: %#v", timeline)
	}
}

func TestFixtureRemoveFailureBecomesResidual(t *testing.T) {
	d := newCRUDMemoryDriver()
	ctx := context.Background()
	fx, err := NewFixture(ctx, d, "unit")
	if err != nil {
		t.Fatal(err)
	}
	// Removing a missing entry must report failure (not panic).
	item, ok := fx.Remove(ctx, drive.Entry{ID: "does-not-exist", Name: "ghost"}, "ghost")
	if ok {
		t.Fatalf("Remove of missing entry reported success: %+v", item)
	}
	if item.Error == "" {
		t.Fatalf("Remove failure lacks error text: %+v", item)
	}
}
