package credentials

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestManagerSharesOneSession(t *testing.T) {
	manager := Open(t.TempDir())
	first := manager.Session(Spec{Name: "main", ParamsSeed: "seed-a"})
	second := manager.Session(Spec{Name: "main", ParamsSeed: "seed-a"})
	if first != second {
		t.Fatal("two mounts of one credential got different session objects")
	}

	notifications := make(chan drive.CredentialSnapshot, 1)
	first.Subscribe(func(snapshot drive.CredentialSnapshot) { notifications <- snapshot })

	snapshot, err := first.Update(0, []byte(`{"cookie":"rotated"}`), "account-a")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 1 || snapshot.Account != "account-a" {
		t.Fatalf("snapshot = %+v, want version 1 with account", snapshot)
	}
	select {
	case got := <-notifications:
		if string(got.Payload) != `{"cookie":"rotated"}` {
			t.Fatalf("notification payload = %s", got.Payload)
		}
	default:
		t.Fatal("sibling mount was not notified")
	}
}

func TestSessionPersistsAcrossManagers(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{Name: "main", ParamsSeed: "seed-a"}
	first := newSession(fileStore{dir: dir}, spec)
	if _, err := first.Update(0, []byte(`{"cookie":"rotated"}`), "account-a"); err != nil {
		t.Fatal(err)
	}

	reloaded := newSession(fileStore{dir: dir}, spec)
	snapshot := reloaded.Snapshot()
	if string(snapshot.Payload) != `{"cookie":"rotated"}` || snapshot.Version != 1 || snapshot.Account != "account-a" {
		t.Fatalf("reloaded snapshot = %+v, want the persisted session", snapshot)
	}
}

func TestSessionParamsSeedChangeInvalidates(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{Name: "main", ParamsSeed: "seed-a"}
	first := newSession(fileStore{dir: dir}, spec)
	if _, err := first.Update(0, []byte(`{"cookie":"account-a"}`), "account-a"); err != nil {
		t.Fatal(err)
	}

	replaced := newSession(fileStore{dir: dir}, Spec{Name: "main", ParamsSeed: "seed-b"})
	snapshot := replaced.Snapshot()
	if len(snapshot.Payload) != 0 {
		t.Fatalf("payload = %s, want an invalidated session", snapshot.Payload)
	}
	if snapshot.Version <= 1 {
		t.Fatalf("version = %d, want it advanced past the old session", snapshot.Version)
	}

	// A manager-level seed change on a live session behaves the same way.
	manager := Open(dir)
	session := manager.Session(Spec{Name: "live", ParamsSeed: "seed-a"})
	if _, err := session.Update(0, []byte(`{"cookie":"old"}`), "account-a"); err != nil {
		t.Fatal(err)
	}
	manager.Session(Spec{Name: "live", ParamsSeed: "seed-b"})
	if payload := session.Snapshot().Payload; len(payload) != 0 {
		t.Fatalf("live payload = %s, want reset", payload)
	}
}

func TestSessionRejectsStaleVersion(t *testing.T) {
	session := newSession(fileStore{dir: t.TempDir()}, Spec{Name: "main", ParamsSeed: "seed"})
	if _, err := session.Update(0, []byte(`{"cookie":"one"}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Update(0, []byte(`{"cookie":"two"}`), ""); !errors.Is(err, drive.ErrCredentialVersionConflict) {
		t.Fatalf("stale update error = %v, want a version conflict", err)
	}
	if string(session.Snapshot().Payload) != `{"cookie":"one"}` {
		t.Fatal("stale update overwrote the session")
	}
}

func TestSessionAdoptsCrossProcessCommit(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{Name: "main", ParamsSeed: "seed"}
	session := newSession(fileStore{dir: dir}, spec)
	if _, err := session.Update(0, []byte(`{"cookie":"one"}`), ""); err != nil {
		t.Fatal(err)
	}

	// Simulate a second process committing a newer session.
	other := newSession(fileStore{dir: dir}, spec)
	if _, err := other.Update(1, []byte(`{"cookie":"two"}`), ""); err != nil {
		t.Fatal(err)
	}

	if _, err := session.Update(1, []byte(`{"cookie":"mine"}`), ""); !errors.Is(err, drive.ErrCredentialVersionConflict) {
		t.Fatalf("stale writer error = %v, want a conflict", err)
	}
	if string(session.Snapshot().Payload) != `{"cookie":"two"}` {
		t.Fatalf("snapshot = %s, want the adopted newer session", session.Snapshot().Payload)
	}
}

func TestSessionRefreshCoalesces(t *testing.T) {
	session := newSession(fileStore{dir: t.TempDir()}, Spec{Name: "main", ParamsSeed: "seed"})
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := session.Refresh(context.Background(), func(drive.CredentialSnapshot) ([]byte, string, error) {
			if calls.Add(1) == 1 {
				close(entered)
			}
			<-release
			return []byte(`{"token":"refreshed"}`), "account-a", nil
		})
		firstDone <- err
	}()
	<-entered

	var wg sync.WaitGroup
	for i := 0; i < 7; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := session.Refresh(context.Background(), func(drive.CredentialSnapshot) ([]byte, string, error) {
				calls.Add(1)
				return []byte(`{"token":"other"}`), "", nil
			})
			if err != nil {
				t.Errorf("refresh: %v", err)
			}
		}()
	}
	// Give the other callers time to join the in-flight refresh before the
	// first one completes; singleflight only coalesces overlapping calls.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if err := <-firstDone; err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want exactly one", calls.Load())
	}
	if string(session.Snapshot().Payload) != `{"token":"refreshed"}` {
		t.Fatalf("session payload = %s, want the shared refresh result", session.Snapshot().Payload)
	}
}

func TestSessionRefreshFailureKeepsSession(t *testing.T) {
	session := newSession(fileStore{dir: t.TempDir()}, Spec{Name: "main", ParamsSeed: "seed"})
	if _, err := session.Update(0, []byte(`{"token":"good"}`), ""); err != nil {
		t.Fatal(err)
	}
	want := errors.New("provider down")
	_, err := session.Refresh(context.Background(), func(drive.CredentialSnapshot) ([]byte, string, error) {
		return nil, "", want
	})
	if !errors.Is(err, want) {
		t.Fatalf("refresh error = %v, want %v", err, want)
	}
	if string(session.Snapshot().Payload) != `{"token":"good"}` || session.Snapshot().Version != 1 {
		t.Fatalf("session changed after a failed refresh: %+v", session.Snapshot())
	}
}

func TestUnsubscribeStopsNotifications(t *testing.T) {
	session := newSession(fileStore{dir: t.TempDir()}, Spec{Name: "main", ParamsSeed: "seed"})
	var calls atomic.Int32
	cancel := session.Subscribe(func(drive.CredentialSnapshot) { calls.Add(1) })
	cancel()
	cancel()
	if _, err := session.Update(0, []byte(`{"cookie":"x"}`), ""); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("notifications = %d after cancel, want 0", calls.Load())
	}
}

func TestParamsSeedIsStable(t *testing.T) {
	first := ParamsSeed("quark", map[string]string{"cookie": "a", "base_url": "https://x"})
	second := ParamsSeed("quark", map[string]string{"base_url": "https://x", "cookie": "a"})
	if first != second {
		t.Fatal("params seed depends on map order")
	}
	if first == ParamsSeed("quark", map[string]string{"cookie": "b", "base_url": "https://x"}) {
		t.Fatal("params seed ignored a changed cookie")
	}
	if first == ParamsSeed("115", map[string]string{"cookie": "a", "base_url": "https://x"}) {
		t.Fatal("params seed ignored the driver type")
	}
}
