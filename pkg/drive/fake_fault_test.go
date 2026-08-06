package drive

import (
	"bytes"
	"context"
	"errors"

	"testing"
	"time"
)

// TestFailAfterLetsEarlierCallsSucceed: only the Nth call fails, which is
// how retry tests land a fault exactly when the retry arrives.
func TestFailAfterLetsEarlierCallsSucceed(t *testing.T) {
	d := NewFakeDriver()
	ctx := context.Background()
	if _, err := d.Mkdir(ctx, "0", "a"); err != nil {
		t.Fatal(err)
	}
	d.FailAfter("Mkdir", 2, errors.New("boom"))
	if _, err := d.Mkdir(ctx, "0", "b"); err != nil {
		t.Fatalf("call 1 must succeed, got %v", err)
	}
	if _, err := d.Mkdir(ctx, "0", "c"); err == nil || err.Error() != "boom" {
		t.Fatalf("call 2 must fail with injected error, got %v", err)
	}
	if _, err := d.Mkdir(ctx, "0", "d"); err != nil {
		t.Fatalf("call 3 must succeed after the fault clears, got %v", err)
	}
}

// TestFailAfterPersistsAcrossInstances: the fault counter lives on the
// driver, so a new VFS instance sharing the driver still hits it. This is
// the shape of "pending journal recovered, then the upload fails again".
func TestFailAfterPersistsAcrossInstances(t *testing.T) {
	d := NewFakeDriver()
	d.FailAfter("PutSource", 1, errors.New("boom"))
	// Two independent "instances" share the driver; the counter must
	// survive, so only the second PutSource can succeed.
	if err := d.putSource(ctx(), d, "f1.txt"); err == nil {
		t.Fatal("first instance PutSource must fail")
	}
	if err := d.putSource(ctx(), d, "f2.txt"); err != nil {
		t.Fatalf("second instance PutSource must succeed, got %v", err)
	}
}

func (d *FakeDriver) putSource(ctx context.Context, _ *FakeDriver, name string) error {
	_, err := d.PutSource(ctx, UploadRequest{
		ParentID: "0",
		Name:     name,
		Source:   memorySource{name: name},
	})
	return err
}

type memorySource struct{ name string }

func (s memorySource) Size() int64 { return int64(len(s.name)) }

func (s memorySource) Open(ctx context.Context) (ReadOnlyFile, error) {
	return nopReadOnly{Reader: bytes.NewReader([]byte(s.name))}, nil
}

type nopReadOnly struct {
	*bytes.Reader
}

func (nopReadOnly) Close() error { return nil }

func ctx() context.Context { return context.Background() }

// TestListStalenessReturnsStaleSnapshot: List lags mutations by the
// configured count, emulating eventually-consistent backends.
func TestListStalenessReturnsStaleSnapshot(t *testing.T) {
	d := NewFakeDriver()
	d.ListStaleness = 2
	ctx := context.Background()
	names := []string{"a", "b", "c"}
	for _, n := range names {
		if _, err := d.Mkdir(ctx, "0", n); err != nil {
			t.Fatal(err)
		}
	}
	// After 3 mutations with staleness 2, List shows mutation 1 (only "a").
	got := listNames(t, d, "0")
	want := []string{"a"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("stale list = %v, want %v", got, want)
	}
	// The latest snapshot is visible through the current tree: resetting
	// staleness to 0 shows everything.
	d.ListStaleness = 0
	got = listNames(t, d, "0")
	if len(got) != len(names) {
		t.Fatalf("current list = %v, want %d entries", got, len(names))
	}
}

func listNames(t *testing.T, d *FakeDriver, parent string) []string {
	t.Helper()
	entries, err := d.List(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names
}

// TestGateBlocksUntilReleased: an in-flight call waits on the gate; sending
// a token releases it; without a token, ctx cancellation aborts it.
func TestGateBlocksUntilReleased(t *testing.T) {
	d := NewFakeDriver()
	gate := make(chan struct{})
	d.Gate = gate
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := d.Mkdir(ctx, "0", "x")
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("call returned before gate release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	gate <- struct{}{} // release one call
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("released call failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not resume after gate release")
	}

	// Second call: cancel instead of releasing.
	go func() {
		_, err := d.Mkdir(ctx, "0", "y")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("call returned before cancel: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel must abort the gated call, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gated call did not abort on cancel")
	}
}
