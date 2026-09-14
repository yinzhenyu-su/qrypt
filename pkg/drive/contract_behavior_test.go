package drive_test

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

// behaviorContractDriver wraps a real driver with one intentional contract
// violation, so tests can prove the checks detect it.
type behaviorContractDriver struct {
	drive.Driver
	listViolation bool
}

func (d *behaviorContractDriver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	entries, err := d.Driver.List(ctx, parentID)
	if err != nil || !d.listViolation {
		return entries, err
	}
	// Leak one extra entry into every listing (a fake direct-children violation).
	return append(entries, drive.Entry{ID: "leaked", Name: "leaked.txt", IsDir: false}), nil
}

// fastBehaviorConvergence collapses the checks' convergence retries for a test
// driver that is synchronous: nothing is converging, so each retry would only
// spend the budget before reporting the verdict it already had. The violating
// test below is the reason this exists - its leak never disappears, so at the
// production step it slept the full 1s + 2s before failing the check.
func fastBehaviorConvergence(t *testing.T) {
	t.Helper()
	restore := drive.BehaviorConvergenceStep
	drive.BehaviorConvergenceStep = time.Millisecond
	t.Cleanup(func() { drive.BehaviorConvergenceStep = restore })
}

func TestBehaviorChecksPassOnLocalfs(t *testing.T) {
	fastBehaviorConvergence(t)
	d := localfs.New(t.TempDir())
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Drop(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	violations := drive.RunBehaviorChecks(ctx, d)
	if len(violations) != 0 {
		t.Fatalf("localfs violated the contract:\n%v", violations)
	}
}

func TestBehaviorChecksDetectListViolation(t *testing.T) {
	fastBehaviorConvergence(t)
	d := &behaviorContractDriver{Driver: localfs.New(t.TempDir()), listViolation: true}
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Drop(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	violations := drive.RunBehaviorChecks(ctx, d)
	found := false
	for _, v := range violations {
		if v.Name == "list_direct_children" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected list_direct_children violation, got %v", violations)
	}
}
