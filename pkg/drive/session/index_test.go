package session

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type countingStateStore struct {
	*drive.FileStateStore
	saves atomic.Int64
}

func (s *countingStateStore) SaveJSON(name string, value any) error {
	s.saves.Add(1)
	return s.FileStateStore.SaveJSON(name, value)
}

func newTestIndex(t *testing.T, maxEntries int) *Index {
	t.Helper()
	return NewIndex(drive.NewFileStateStore(filepath.Join(t.TempDir(), "state")), "sessions.json", IndexOptions{
		MaxEntries: maxEntries,
		OnError: func(err error) {
			t.Errorf("unexpected index error: %v", err)
		},
	})
}

func TestIdentityKeyChangesWhenContentChanges(t *testing.T) {
	base := Identity{ParentID: "p", Name: "n", Size: 10, Fingerprint: "abc"}
	if base.Key() != base.Key() {
		t.Fatal("key must be stable for the same identity")
	}
	mutations := []struct {
		name string
		run  func(*Identity)
	}{
		{"size", func(id *Identity) { id.Size++ }},
		{"fingerprint", func(id *Identity) { id.Fingerprint = "def" }},
		{"name", func(id *Identity) { id.Name = "n2" }},
		{"parent", func(id *Identity) { id.ParentID = "p2" }},
	}
	for _, m := range mutations {
		changed := base
		m.run(&changed)
		if changed.Key() == base.Key() {
			t.Fatalf("key must change when %s changes", m.name)
		}
	}
}

func TestIdentityResumableRequiresFingerprint(t *testing.T) {
	noFingerprint := Identity{Size: 10}
	if noFingerprint.Resumable() {
		t.Fatal("identity without a content fingerprint must not be resumable")
	}
	withFingerprint := Identity{Size: 10, Fingerprint: "abc"}
	if !withFingerprint.Resumable() {
		t.Fatal("identity with sha256 must be resumable")
	}
}

func TestIndexCreateGetDeletePersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	store := drive.NewFileStateStore(filepath.Join(dir, "state"))
	index := NewIndex(store, "sessions.json", IndexOptions{})
	token := json.RawMessage(`{"upload_id":"u-1"}`)
	if err := index.Create("k1", token); err != nil {
		t.Fatal(err)
	}
	if err := index.Create("k2", json.RawMessage(`{"upload_id":"u-2"}`)); err != nil {
		t.Fatal(err)
	}

	reloaded := NewIndex(drive.NewFileStateStore(filepath.Join(dir, "state")), "sessions.json", IndexOptions{})
	s, ok := reloaded.Get("k1")
	if !ok {
		t.Fatal("expected binding to survive a new index instance")
	}
	if !sameToken(t, s.Token, token) {
		t.Fatalf("token = %s, want %s", s.Token, token)
	}
	if s.CreatedAt.IsZero() || !s.CreatedAt.Equal(s.UpdatedAt) {
		t.Fatalf("timestamps = created %v updated %v", s.CreatedAt, s.UpdatedAt)
	}

	reloaded.Delete("k1")
	if _, ok := reloaded.Get("k1"); ok {
		t.Fatal("expected deleted binding to be gone")
	}
	still := NewIndex(drive.NewFileStateStore(filepath.Join(dir, "state")), "sessions.json", IndexOptions{})
	if _, ok := still.Get("k1"); ok {
		t.Fatal("expected deleted binding to stay gone on disk")
	}
	if _, ok := still.Get("k2"); !ok {
		t.Fatal("expected unrelated binding to survive")
	}
}

func TestIndexCreateOverwritesToken(t *testing.T) {
	index := newTestIndex(t, 10)
	first := json.RawMessage(`{"upload_id":"old"}`)
	if err := index.Create("k", first); err != nil {
		t.Fatal(err)
	}
	second := json.RawMessage(`{"upload_id":"new"}`)
	if err := index.Create("k", second); err != nil {
		t.Fatal(err)
	}
	s, _ := index.Get("k")
	if string(s.Token) != string(second) {
		t.Fatalf("token = %s, want %s", s.Token, second)
	}
}

func TestIndexTouchThrottlesWrites(t *testing.T) {
	dir := t.TempDir()
	store := &countingStateStore{FileStateStore: drive.NewFileStateStore(filepath.Join(dir, "state"))}
	index := NewIndex(store, "sessions.json", IndexOptions{TouchWriteInterval: time.Hour})
	if err := index.Create("k", json.RawMessage(`{"upload_id":"u"}`)); err != nil {
		t.Fatal(err)
	}
	before := store.saves.Load()
	for i := 0; i < 50; i++ {
		index.Touch("k")
	}
	after := store.saves.Load()
	if after-before > 1 {
		t.Fatalf("touch persisted %d times within interval, want at most 1", after-before)
	}
}

func TestRunExpirerNonPositiveInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	for _, interval := range []time.Duration{0, -time.Second} {
		RunExpirer(ctx, interval, func() { ran = true })
	}
	if ran {
		t.Fatal("expirer must not run with a non-positive interval")
	}
}

type flakyStateStore struct {
	*drive.FileStateStore
	fail atomic.Bool
}

func (s *flakyStateStore) SaveJSON(name string, value any) error {
	if s.fail.Load() {
		return errors.New("disk full")
	}
	return s.FileStateStore.SaveJSON(name, value)
}

func TestIndexCreateWriteFailureLeavesNoTrace(t *testing.T) {
	dir := t.TempDir()
	store := &flakyStateStore{FileStateStore: drive.NewFileStateStore(filepath.Join(dir, "state"))}
	index := NewIndex(store, "sessions.json", IndexOptions{})
	if err := index.Create("k1", json.RawMessage(`{"upload_id":"u1"}`)); err != nil {
		t.Fatal(err)
	}
	store.fail.Store(true)
	if err := index.Create("k2", json.RawMessage(`{"upload_id":"u2"}`)); err == nil {
		t.Fatal("expected a failed disk write to surface as an error")
	}
	store.fail.Store(false)
	if _, ok := index.Get("k2"); ok {
		t.Fatal("failed create must not leak into memory")
	}
	reloaded := NewIndex(drive.NewFileStateStore(filepath.Join(dir, "state")), "sessions.json", IndexOptions{})
	if _, ok := reloaded.Get("k2"); ok {
		t.Fatal("failed create must not reach disk")
	}
	if _, ok := reloaded.Get("k1"); !ok {
		t.Fatal("unrelated binding must survive")
	}
}

func TestIndexDeleteWriteFailureKeepsBinding(t *testing.T) {
	dir := t.TempDir()
	store := &flakyStateStore{FileStateStore: drive.NewFileStateStore(filepath.Join(dir, "state"))}
	index := NewIndex(store, "sessions.json", IndexOptions{})
	if err := index.Create("k1", json.RawMessage(`{"upload_id":"u1"}`)); err != nil {
		t.Fatal(err)
	}
	store.fail.Store(true)
	index.Delete("k1")
	store.fail.Store(false)
	if _, ok := index.Get("k1"); !ok {
		t.Fatal("failed delete must keep the binding in memory")
	}
	index.Delete("k1")
	if _, ok := index.Get("k1"); ok {
		t.Fatal("binding must be gone after a successful delete")
	}
}

func TestIndexFlushPersistsTouchedTimestamp(t *testing.T) {
	dir := t.TempDir()
	store := drive.NewFileStateStore(filepath.Join(dir, "state"))
	index := NewIndex(store, "sessions.json", IndexOptions{TouchWriteInterval: time.Hour})
	if err := index.Create("k", json.RawMessage(`{"upload_id":"u"}`)); err != nil {
		t.Fatal(err)
	}
	index.Touch("k") // 节流期内不写盘：内存时间戳领先磁盘
	mem, _ := index.Get("k")
	before := NewIndex(store, "sessions.json", IndexOptions{})
	disk, _ := before.Get("k")
	if !mem.UpdatedAt.After(disk.UpdatedAt) {
		t.Fatal("touch must advance memory ahead of disk while throttled")
	}
	if err := index.Flush(); err != nil {
		t.Fatal(err)
	}
	after := NewIndex(store, "sessions.json", IndexOptions{})
	persisted, _ := after.Get("k")
	if !persisted.UpdatedAt.Equal(mem.UpdatedAt) {
		t.Fatalf("flush must persist the in-memory timestamp: disk %v, want %v", persisted.UpdatedAt, mem.UpdatedAt)
	}
}

func TestIndexTouchWithConcurrentBitmapMarksDoNotLoseParts(t *testing.T) {
	dir := t.TempDir()
	store := drive.NewFileStateStore(filepath.Join(dir, "state"))
	index := NewIndex(store, "sessions.json", IndexOptions{TouchWriteInterval: time.Hour})
	if err := index.Create("k", json.RawMessage(`{"confirmed":null}`)); err != nil {
		t.Fatal(err)
	}
	// 64 个 goroutine 并发确认不同分片：闭包在 Index 锁内做读-改-写，
	// 任何确认都不允许丢失。
	var wg sync.WaitGroup
	for part := 1; part <= 64; part++ {
		wg.Add(1)
		go func(part int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				index.TouchWith("k", func(s *Session) {
					var v struct {
						Confirmed []byte `json:"confirmed"`
					}
					_ = json.Unmarshal(s.Token, &v)
					v.Confirmed = ConfirmedBitmap(64<<20, 1<<20, v.Confirmed)
					MarkConfirmed(v.Confirmed, part)
					s.Token, _ = json.Marshal(v)
				})
			}
		}(part)
	}
	wg.Wait()
	s, _ := index.Get("k")
	var v struct {
		Confirmed []byte `json:"confirmed"`
	}
	_ = json.Unmarshal(s.Token, &v)
	done := ConfirmedParts(v.Confirmed)
	for part := 1; part <= 64; part++ {
		if !done[part] {
			t.Fatalf("part %d confirmation lost under concurrent updates", part)
		}
	}
}

func TestIndexExpireReclaimsAndKeepsOnFailure(t *testing.T) {
	index := NewIndex(drive.NewFileStateStore(filepath.Join(t.TempDir(), "state")), "sessions.json", IndexOptions{})
	now := time.Now()
	if err := index.Create("old", json.RawMessage(`{"upload_id":"u-old"}`)); err != nil {
		t.Fatal(err)
	}
	if err := index.Create("new", json.RawMessage(`{"upload_id":"u-new"}`)); err != nil {
		t.Fatal(err)
	}
	index.mu.Lock()
	old := index.sessions["old"]
	old.UpdatedAt = now.Add(-48 * time.Hour)
	index.sessions["old"] = old
	fresh := index.sessions["new"]
	fresh.UpdatedAt = now
	index.sessions["new"] = fresh
	index.mu.Unlock()

	var reclaimed []string
	failNext := true
	index.Expire(24*time.Hour, now, func(s Session) error {
		reclaimed = append(reclaimed, s.Key)
		if failNext {
			failNext = false
			return errors.New("provider unreachable")
		}
		return nil
	})

	// The failed reclaim keeps the binding for the next pass.
	if _, ok := index.Get("old"); !ok {
		t.Fatal("binding must survive a failed reclaim")
	}
	if _, ok := index.Get("new"); !ok {
		t.Fatal("fresh binding must not expire")
	}
	if len(reclaimed) != 1 || reclaimed[0] != "old" {
		t.Fatalf("reclaimed = %v, want [old]", reclaimed)
	}

	index.Expire(24*time.Hour, now, func(s Session) error { return nil })
	if _, ok := index.Get("old"); ok {
		t.Fatal("binding must be dropped after a successful reclaim")
	}
}

func TestIndexExpireLeavesLiveBindingAlone(t *testing.T) {
	index := newTestIndex(t, 10)
	now := time.Now()
	if err := index.Create("k", json.RawMessage(`{"upload_id":"u"}`)); err != nil {
		t.Fatal(err)
	}
	index.Touch("k") // fresh timestamp
	index.Expire(24*time.Hour, now, func(Session) error {
		t.Fatal("reclaim must not run for a touched binding")
		return nil
	})
	if _, ok := index.Get("k"); !ok {
		t.Fatal("touched binding must survive expiry")
	}
}

func TestIndexEvictsOldestOverCap(t *testing.T) {
	index := newTestIndex(t, 2)
	now := time.Now()
	if err := index.Create("a", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := index.Create("b", nil); err != nil {
		t.Fatal(err)
	}
	index.mu.Lock()
	entryA := index.sessions["a"]
	entryA.UpdatedAt = now.Add(-time.Hour)
	index.sessions["a"] = entryA
	index.mu.Unlock()
	if err := index.Create("c", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := index.Get("a"); ok {
		t.Fatal("oldest binding must be evicted over the cap")
	}
	if _, ok := index.Get("c"); !ok {
		t.Fatal("new binding must survive")
	}
}

func TestIndexListCopiesTokens(t *testing.T) {
	index := newTestIndex(t, 10)
	if err := index.Create("k", json.RawMessage(`{"upload_id":"u"}`)); err != nil {
		t.Fatal(err)
	}
	list := index.List()
	if len(list) != 1 {
		t.Fatalf("list = %d entries, want 1", len(list))
	}
	list[0].Token[7] = 'x' // mutate the returned copy
	s, _ := index.Get("k")
	if string(s.Token) != `{"upload_id":"u"}` {
		t.Fatalf("token = %s, want untouched internal state", s.Token)
	}
}

func sameToken(t *testing.T, got, want json.RawMessage) bool {
	t.Helper()
	var gotMap, wantMap map[string]string
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatalf("unmarshal got token: %v", err)
	}
	if err := json.Unmarshal(want, &wantMap); err != nil {
		t.Fatalf("unmarshal want token: %v", err)
	}
	if gotMap["upload_id"] != wantMap["upload_id"] {
		return false
	}
	return true
}

func TestIndexNilSafe(t *testing.T) {
	var index *Index
	if _, ok := index.Get("k"); ok {
		t.Fatal("nil index must not return bindings")
	}
	if err := index.Create("k", nil); err != nil {
		t.Fatal(err)
	}
	index.Delete("k")
	index.Touch("k")
	index.Expire(time.Hour, time.Now(), nil)
	if list := index.List(); list != nil {
		t.Fatalf("nil index list = %v", list)
	}
}
