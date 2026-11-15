package quark

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func mustAddr(t *testing.T, value string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(value)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestEdgeHealthPenaltyExpiry(t *testing.T) {
	health := newEdgeHealth(time.Minute)
	if health.hasPenalties() {
		t.Fatal("fresh edge health reports penalties")
	}
	health.penalize("1.2.3.4", "test")
	if !health.hasPenalties() {
		t.Fatal("penalty was not recorded")
	}
	if !health.isBad(mustAddr(t, "1.2.3.4"), time.Now()) {
		t.Fatal("penalized edge is not reported bad")
	}
	health.mu.Lock()
	health.bad["1.2.3.4"] = edgePenalty{until: time.Now().Add(-time.Second), reason: "test"}
	health.mu.Unlock()
	if health.hasPenalties() {
		t.Fatal("expired penalty still reported")
	}
}

func TestEdgeHealthDebugSnapshot(t *testing.T) {
	health := newEdgeHealth(time.Minute)
	health.penalize("1.2.3.4", "body read failed")
	snapshot := health.debugSnapshot(time.Now())
	if len(snapshot) != 1 || snapshot[0]["ip"] != "1.2.3.4" || snapshot[0]["reason"] != "body read failed" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	health.mu.Lock()
	health.bad["1.2.3.4"] = edgePenalty{until: time.Now().Add(-time.Second), reason: "expired"}
	health.mu.Unlock()
	if got := health.debugSnapshot(time.Now()); len(got) != 0 {
		t.Fatalf("expired penalty still visible: %#v", got)
	}
}

func TestEdgeDialDelegatesWithoutPenalties(t *testing.T) {
	health := newEdgeHealth(time.Minute)
	var dialed []string
	dial := health.dial(func(_ context.Context, _ string, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, nil
	})
	if _, err := dial(context.Background(), "tcp", "dl.test:443"); err != nil {
		t.Fatal(err)
	}
	if len(dialed) != 1 || dialed[0] != "dl.test:443" {
		t.Fatalf("dials = %v, want the hostname unchanged", dialed)
	}
}

func TestEdgeDialSkipsPenalizedEdge(t *testing.T) {
	health := newEdgeHealth(time.Minute)
	health.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{mustAddr(t, "1.1.1.1"), mustAddr(t, "2.2.2.2")}, nil
	}
	health.penalize("1.1.1.1", "test")
	var dialed []string
	dial := health.dial(func(_ context.Context, _ string, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, nil
	})
	if _, err := dial(context.Background(), "tcp", "dl.test:443"); err != nil {
		t.Fatal(err)
	}
	if len(dialed) != 1 || dialed[0] != "2.2.2.2:443" {
		t.Fatalf("dials = %v, want only the healthy edge", dialed)
	}
}

func TestEdgeDialFallsBackWhenAllPenalized(t *testing.T) {
	health := newEdgeHealth(time.Minute)
	health.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{mustAddr(t, "1.1.1.1"), mustAddr(t, "2.2.2.2")}, nil
	}
	health.penalize("1.1.1.1", "test")
	health.penalize("2.2.2.2", "test")
	var dialed []string
	dial := health.dial(func(_ context.Context, _ string, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, nil
	})
	if _, err := dial(context.Background(), "tcp", "dl.test:443"); err != nil {
		t.Fatal(err)
	}
	if len(dialed) != 1 || dialed[0] != "dl.test:443" {
		t.Fatalf("dials = %v, want a normal-resolution fallback", dialed)
	}
}

func TestEdgeDialPenalizesTimeout(t *testing.T) {
	health := newEdgeHealth(time.Minute)
	health.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{mustAddr(t, "1.1.1.1"), mustAddr(t, "2.2.2.2")}, nil
	}
	health.penalize("9.9.9.9", "keep the candidate path active")
	var dialed []string
	dial := health.dial(func(_ context.Context, _ string, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		if address == "1.1.1.1:443" {
			return nil, timeoutError{}
		}
		return nil, nil
	})
	if _, err := dial(context.Background(), "tcp", "dl.test:443"); err != nil {
		t.Fatal(err)
	}
	if len(dialed) != 2 {
		t.Fatalf("dials = %v, want the failed edge then the healthy one", dialed)
	}
	if !health.isBad(mustAddr(t, "1.1.1.1"), time.Now()) {
		t.Fatal("timed-out edge was not penalized")
	}
}

// TestDriverPenalizesEdgeOnSlowBody: the slow-read watchdog reports the remote
// edge, which must be remembered for later dials.
func TestDriverPenalizesEdgeOnSlowBody(t *testing.T) {
	driver := New("k=v", Options{})
	driver.noteDownloadBodyError("1.2.3.4:443", errDownloadTooSlow, 1<<10, 30*time.Second)
	if !driver.cl.edges.isBad(mustAddr(t, "1.2.3.4"), time.Now()) {
		t.Fatal("slow body did not penalize its edge")
	}
}
