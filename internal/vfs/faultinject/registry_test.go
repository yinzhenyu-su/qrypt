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
	if match.Handle.FaultID == "" || match.Handle.Token == 0 {
		t.Fatal("once match must carry a claim", match.Handle)
	}
	// While claimed, the same rule cannot be matched again.
	if _, ok := reg.Match(now, "/file.txt", ""); ok {
		t.Fatal("once fault still matched while claimed")
	}
	// Firing consumes the rule permanently.
	reg.Complete(match.Handle, now)
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
	reg.Release(claimed.Handle)
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
	reg.Release(first.Handle)
	second, ok := reg.Match(time.Now(), "/file.txt", "")
	if !ok || second.Handle.FaultID == "" {
		t.Fatal("released rule not matchable again")
	}
	reg.Complete(second.Handle, time.Now())
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
	reg.Release(first.Handle) // upload A ends without firing
	second, ok := reg.Match(time.Now(), "/file.txt", "")
	if !ok {
		t.Fatal("second match failed")
	}
	// Upload A's cleanup runs late with its old claim: must NOT release B.
	reg.Release(first.Handle)
	if _, ok := reg.Match(time.Now(), "/file.txt", ""); ok {
		t.Fatal("stale release un-claimed upload B's claim")
	}
	reg.Complete(second.Handle, time.Now())
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
		if match.Handle.Token != 0 || match.Handle.Once {
			t.Fatalf("non-once match must carry no claim token, got %+v", match.Handle)
		}
		reg.Complete(match.Handle, time.Now())
	}
	if got := reg.Faults(time.Now()); len(got) != 1 {
		t.Fatalf("repeated fault should stay registered, got %+v", got)
	}
	if got := reg.Faults(time.Now()); !got[0].Fired {
		t.Fatal("repeated fault should show fired after Complete")
	}
}

// TestConcurrentClaimsOnDistinctFaultsDoNotInterfere: two once-rules both
// claimed with the SAME local token (each starts at 1) must be completed
// independently - completing A never consumes B and vice versa.
func TestConcurrentClaimsOnDistinctFaultsDoNotInterfere(t *testing.T) {
	reg := NewRegistry(0)
	if _, err := reg.Inject(InjectRequest{Path: "/a.txt", AfterBytes: 1, Once: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Inject(InjectRequest{Path: "/b.txt", AfterBytes: 1, Once: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a, ok := reg.Match(now, "/a.txt", "")
	if !ok {
		t.Fatal("match a failed")
	}
	b, ok := reg.Match(now, "/b.txt", "")
	if !ok {
		t.Fatal("match b failed")
	}
	if a.Handle.FaultID == b.Handle.FaultID || a.Handle.Token != b.Handle.Token {
		t.Fatalf("premise broken: claims must share the token but differ in id: a=%+v b=%+v", a.Handle, b.Handle)
	}

	// Completing A must NOT consume B: releasing B's own handle makes it
	// matchable again (the re-match below claims it again).
	reg.Complete(a.Handle, now)
	reg.Release(b.Handle)
	b2, ok := reg.Match(now, "/b.txt", "")
	if !ok {
		t.Fatal("completing A interfered with B: B not matchable after its own release")
	}

	// Completing B with A's handle (wrong id, same token) must NOT
	// consume B: after releasing B's new claim, B is still registered.
	reg.Complete(a.Handle, now) // wrong id for B
	reg.Release(b2.Handle)
	b3, ok := reg.Match(now, "/b.txt", "")
	if !ok {
		t.Fatal("completing B with A's handle consumed B")
	}

	// B's own handle completes it for real.
	reg.Complete(b3.Handle, now)
	if _, ok := reg.Match(now, "/b.txt", ""); ok {
		t.Fatal("B still matchable after its own completion")
	}
}

func TestCompleteHandleTable(t *testing.T) {
	now := time.Now()

	matchPath := func(t *testing.T, reg *Registry, path string) MatchResult {
		t.Helper()
		r, ok := reg.Match(now, path, "")
		if !ok {
			t.Fatalf("match %s failed", path)
		}
		return r
	}
	releaseAndRematch := func(t *testing.T, reg *Registry, path string, handle MatchHandle) bool {
		t.Helper()
		reg.Release(handle)
		_, ok := reg.Match(now, path, "")
		return ok
	}

	t.Run("correct id and token consumes", func(t *testing.T) {
		reg := NewRegistry(0)
		reg.Inject(InjectRequest{Path: "/a.txt", AfterBytes: 1, Once: true})
		r := matchPath(t, reg, "/a.txt")
		reg.Complete(r.Handle, now)
		if releaseAndRematch(t, reg, "/a.txt", r.Handle) {
			t.Fatal("rule still matchable after correct completion")
		}
	})

	t.Run("wrong id same token does not consume", func(t *testing.T) {
		reg := NewRegistry(0)
		reg.Inject(InjectRequest{Path: "/a.txt", AfterBytes: 1, Once: true})
		reg.Inject(InjectRequest{Path: "/b.txt", AfterBytes: 1, Once: true})
		a := matchPath(t, reg, "/a.txt")
		b := matchPath(t, reg, "/b.txt")
		if a.Handle.Token != b.Handle.Token {
			t.Fatalf("tokens differ (a=%d b=%d), test premise broken", a.Handle.Token, b.Handle.Token)
		}
		// Complete B with A's handle: wrong id, equal token.
		reg.Complete(a.Handle, now)
		if !releaseAndRematch(t, reg, "/b.txt", b.Handle) {
			t.Fatal("B consumed by A's handle - wrong-id completion must be rejected")
		}
	})

	t.Run("stale token does not consume", func(t *testing.T) {
		reg := NewRegistry(0)
		reg.Inject(InjectRequest{Path: "/a.txt", AfterBytes: 1, Once: true})
		first := matchPath(t, reg, "/a.txt")
		reg.Release(first.Handle)
		second := matchPath(t, reg, "/a.txt")
		if first.Handle.Token == second.Handle.Token {
			t.Fatal("token not monotonic, test premise broken")
		}
		// Complete with the STALE first token.
		reg.Complete(first.Handle, now)
		if !releaseAndRematch(t, reg, "/a.txt", second.Handle) {
			t.Fatal("rule consumed by stale token - must be rejected")
		}
	})

	t.Run("non-once empty handle records fired", func(t *testing.T) {
		reg := NewRegistry(0)
		reg.Inject(InjectRequest{Path: "/rep.txt", AfterBytes: 1, Once: false})
		r := matchPath(t, reg, "/rep.txt")
		if r.Handle.Once {
			t.Fatal("non-once match must not set Once")
		}
		reg.Complete(r.Handle, now)
		faults := reg.Faults(now)
		if len(faults) != 1 {
			t.Fatalf("faults = %+v, want exactly the repeated rule", faults)
		}
		if !faults[0].Fired || faults[0].FiredAt.IsZero() {
			t.Fatalf("repeated rule should show fired: %+v", faults[0])
		}
	})
}
