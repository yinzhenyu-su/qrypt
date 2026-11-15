package faultinject

import (
	"testing"
	"time"
)

func TestReadFaultRegistryMatchesOffsetAndConsumes(t *testing.T) {
	registry := NewReadFaultRegistry()
	fault, err := registry.Inject(DebugReadFaultRequest{
		Path: "/cloud/movie.mp4", EntryID: "entry-1", Kind: ReadFaultStall,
		Offset: 4 << 20, Length: 512 << 10, AfterBytes: 1024, Times: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fault.ID == "" || fault.Remaining != 2 {
		t.Fatalf("unexpected fault: %+v", fault)
	}
	if _, ok := registry.MatchReadFault("other-entry", 4<<20, 512<<10); ok {
		t.Fatal("fault matched a different entry")
	}
	if _, ok := registry.MatchReadFault("entry-1", 0, 512<<10); ok {
		t.Fatal("fault matched a range before its offset")
	}
	if got, ok := registry.MatchReadFault("entry-1", 4<<20, 512<<10); !ok || got.Remaining != 2 {
		t.Fatalf("first match = %+v, %v; want remaining 2 before consume", got, ok)
	}
	registry.ConsumeReadFault(fault.ID)
	if list := registry.Faults(); len(list) != 1 || list[0].Remaining != 1 {
		t.Fatalf("remaining after first consume = %+v, want 1", list)
	}
	if _, ok := registry.MatchReadFault("entry-1", 4<<20, 512<<10); !ok {
		t.Fatal("second match did not fire")
	}
	registry.ConsumeReadFault(fault.ID)
	if _, ok := registry.MatchReadFault("entry-1", 4<<20, 512<<10); ok {
		t.Fatal("fault fired more than --times")
	}
}

func TestReadFaultRegistryAnyOffsetAndClear(t *testing.T) {
	registry := NewReadFaultRegistry()
	fault, err := registry.Inject(DebugReadFaultRequest{
		Path: "/cloud/movie.mp4", EntryID: "entry-1", Kind: ReadFaultStatus, AnyOffset: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fault.Status != 500 {
		t.Fatalf("default status = %d, want 500", fault.Status)
	}
	if _, ok := registry.MatchReadFault("entry-1", 42, 512<<10); !ok {
		t.Fatal("any-offset fault did not match")
	}
	registry.ConsumeReadFault(fault.ID)
	registry.Clear(fault.ID)
	if len(registry.Faults()) != 0 {
		t.Fatal("cleared fault remains listed")
	}
	if _, err := registry.Inject(DebugReadFaultRequest{Path: "/a", EntryID: "a", Kind: ReadFaultDelay, Delay: "10ms"}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Inject(DebugReadFaultRequest{Path: "/b", EntryID: "b", Kind: ReadFaultTruncate}); err != nil {
		t.Fatal(err)
	}
	registry.Clear("")
	if len(registry.Faults()) != 0 {
		t.Fatal("clear-all left faults behind")
	}
}

func TestReadFaultRegistryValidatesKinds(t *testing.T) {
	registry := NewReadFaultRegistry()
	cases := []DebugReadFaultRequest{
		{Path: "/a", EntryID: "a"},
		{Path: "/a", EntryID: "a", Kind: "bogus"},
		{Path: "/a", EntryID: "a", Kind: ReadFaultStatus, Status: 99},
		{Path: "/a", EntryID: "a", Kind: ReadFaultDelay},
		{Kind: ReadFaultStall},
	}
	for _, req := range cases {
		if _, err := registry.Inject(req); err == nil {
			t.Fatalf("request %+v was accepted", req)
		}
	}
	if _, err := registry.Inject(DebugReadFaultRequest{Path: "/a", EntryID: "a", Kind: ReadFaultDelay, Delay: (time.Millisecond).String()}); err != nil {
		t.Fatalf("valid delay rejected: %v", err)
	}
}
