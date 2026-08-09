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
	if match.ClaimToken == 0 {
		t.Fatal("once match must carry a claim token")
	}
	// While claimed, the same rule cannot be matched again.
	if _, ok := reg.Match(now, "/file.txt", ""); ok {
		t.Fatal("once fault still matched while claimed")
	}
	// Firing consumes the rule permanently.
	reg.Fire(match.ClaimToken)
	if _, ok := reg.Match(now, "/file.txt", ""); ok {
		t.Fatal("fired once fault still matched")
	}
}

// TestConcurrentOnceMatchClaimsExactlyOnce: two goroutines racing to match
// the same once rule - exactly one wins the claim.
func TestConcurrentOnceMatchClaimsExactlyOnce(t *testing.T) {
	reg := NewRegistry(0)
	if _, err := reg.Inject(InjectRequest{Path: "/file.txt", Phase: "uploading", Once: true}); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	var wg sync.WaitGroup
	wins := make(chan MatchResult, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result, ok := reg.Match(time.Now(), "/file.txt", ""); ok {
				wins <- result
			}
		}()
	}
	wg.Wait()
	close(wins)
	var claimed MatchResult
	count := 0
	for r := range wins {
		claimed = r
		count++
	}
	if count != 1 {
		t.Fatalf("concurrent once matches = %d, want exactly 1", count)
	}
	// The winner can release the claim so a later upload can retry.
	reg.Release(claimed.ClaimToken)
	if _, ok := reg.Match(time.Now(), "/file.txt", ""); !ok {
		t.Fatal("released once fault should be matchable again")
	}
}

// TestOnceClaimReleaseReclaims: an upload that matches but never fires
// (bytes/delay threshold not reached) returns the rule to armed.
func TestOnceClaimReleaseReclaims(t *testing.T) {
	reg := NewRegistry(0)
	if _, err := reg.Inject(InjectRequest{Path: "/file.txt", AfterBytes: 1 << 20, Once: true}); err != nil {
		t.Fatal(err)
	}
	first, ok := reg.Match(time.Now(), "/file.txt", "")
	if !ok {
		t.Fatal("first match failed")
	}
	reg.Release(first.ClaimToken)
	second, ok := reg.Match(time.Now(), "/file.txt", "")
	if !ok || second.ClaimToken == 0 {
		t.Fatal("released rule not matchable again")
	}
	reg.Fire(second.ClaimToken)
	if _, ok := reg.Match(time.Now(), "/file.txt", ""); ok {
		t.Fatal("fired rule still matchable")
	}
}

// TestOnceClaimStaleReleaseDoesNotAffectNewClaim: a stale cleanup holding
// an old token must not release a claim re-issued to a newer upload.
func TestOnceClaimStaleReleaseDoesNotAffectNewClaim(t *testing.T) {
	reg := NewRegistry(0)
	if _, err := reg.Inject(InjectRequest{Path: "/file.txt", AfterBytes: 1, Once: true}); err != nil {
		t.Fatal(err)
	}
	first, ok := reg.Match(time.Now(), "/file.txt", "")
	if !ok {
		t.Fatal("first match failed")
	}
	reg.Release(first.ClaimToken) // upload A ends without firing
	second, ok := reg.Match(time.Now(), "/file.txt", "")
	if !ok {
		t.Fatal("second match failed")
	}
	// Upload A's cleanup runs late with its old token: must NOT release B.
	reg.Release(first.ClaimToken)
	if _, ok := reg.Match(time.Now(), "/file.txt", ""); ok {
		t.Fatal("stale release un-claimed upload B's claim")
	}
	reg.Fire(second.ClaimToken)
}

// TestRepeatedFaultMatchesMultipleTimes: a non-once fault can be matched
// and fired repeatedly.
func TestRepeatedFaultMatchesMultipleTimes(t *testing.T) {
	reg := NewRegistry(0)
	if _, err := reg.Inject(InjectRequest{Path: "/file.txt", AfterBytes: 1, Once: false}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		match, ok := reg.Match(time.Now(), "/file.txt", "")
		if !ok {
			t.Fatalf("match %d failed", i)
		}
		if match.ClaimToken != 0 {
			t.Fatalf("non-once match must carry no claim token, got %d", match.ClaimToken)
		}
		reg.MarkFired(match.ID, time.Now())
	}
	if got := reg.Faults(time.Now()); len(got) != 1 {
		t.Fatalf("repeated fault should stay registered, got %+v", got)
	}
	if got := reg.Faults(time.Now()); !got[0].Fired {
		t.Fatal("repeated fault should show fired after MarkFired")
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
