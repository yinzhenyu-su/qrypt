package faultinject

import (
	"sync"
	"testing"
	"time"
)

func TestRegistryOwnsCancelFaultState(t *testing.T) {
	reg := NewRegistry(0)
	now := time.Now()

	// Match requires the fault to be registered; construct expired/active
	// faults directly to keep the test focused on matching semantics.
	reg.faults["expired"] = &Fault{
		ID: "expired", Path: "/old.txt", Once: true,
		CreatedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Minute),
	}
	reg.faults["active"] = &Fault{
		ID: "active", Path: "/file.txt", Once: true,
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}

	faults := reg.Faults(now)
	if len(faults) != 1 || faults[0].ID != "active" {
		t.Fatalf("faults = %+v, want only active", faults)
	}
	if _, ok := reg.Match(now, "/missing.txt", ""); ok {
		t.Fatal("unexpected match for missing Path")
	}
	match, ok := reg.Match(now, "/file.txt", "")
	if !ok || match.ID != "active" || match.MatchedPath != "/file.txt" {
		t.Fatalf("match = %+v ok=%v, want active", match, ok)
	}
	// Once rules are claimed atomically at Match time.
	if _, ok := reg.Match(now, "/file.txt", ""); ok {
		t.Fatal("once-fired fault still matched after claim")
	}
}

// TestConcurrentOnceMatchClaimsExactlyOnce: two goroutines racing to match
// the same once rule - exactly one wins.
func TestConcurrentOnceMatchClaimsExactlyOnce(t *testing.T) {
	reg := NewRegistry(0)
	id, err := reg.Inject(InjectRequest{Path: "/file.txt", Phase: "uploading", Once: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = id

	const workers = 8
	var wg sync.WaitGroup
	wins := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result, ok := reg.Match(time.Now(), "/file.txt", ""); ok {
				wins <- result.ID
			}
		}()
	}
	wg.Wait()
	close(wins)
	if got := len(wins); got != 1 {
		t.Fatalf("concurrent once matches = %d, want exactly 1", got)
	}
}

func TestRegistryInjectValidatesAndGeneratesIDs(t *testing.T) {
	reg := NewRegistry(0)
	if _, err := reg.Inject(InjectRequest{}); err == nil {
		t.Fatal("empty inject should fail (no path/op_id)")
	}
	if _, err := reg.Inject(InjectRequest{Path: "/a.txt"}); err == nil {
		t.Fatal("inject without phase/bytes/delay should fail")
	}
	id, err := reg.Inject(InjectRequest{Path: "/a.txt", AfterBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("inject returned empty id")
	}
	id2, err := reg.Inject(InjectRequest{OpID: "fid", Phase: "uploading"})
	if err != nil {
		t.Fatal(err)
	}
	if id == id2 {
		t.Fatal("ids must be unique")
	}
	if err := reg.Clear(id); err != nil {
		t.Fatal(err)
	}
	if err := reg.Clear("nope"); err == nil {
		t.Fatal("clearing unknown id should fail")
	}
	if got := reg.Faults(time.Now()); len(got) != 1 {
		t.Fatalf("faults = %+v, want 1 after clear", got)
	}
}
