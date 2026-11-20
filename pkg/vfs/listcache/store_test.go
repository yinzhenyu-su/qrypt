package listcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "lists"), "m1", "id-1", time.Minute, 1<<20)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if store == nil {
		t.Fatal("store disabled")
	}
	return store
}

func entries() []drive.Entry {
	return []drive.Entry{{ID: "1", Name: "a.txt", Size: 1}, {ID: "2", Name: "sub", IsDir: true}}
}

func TestStoreHitAndMiss(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	if _, ok := store.Get("/dir", now); ok {
		t.Fatal("empty store hit")
	}
	store.Put("/dir", entries(), 7, now)
	got, ok := store.Get("/dir", now.Add(time.Second))
	if !ok || len(got) != 2 || got[1].Name != "sub" {
		t.Fatalf("hit = %v %v", got, ok)
	}
	if _, ok := store.Get("/other", now); ok {
		t.Fatal("unknown path hit")
	}
	if stats := store.Stats(); stats.Hits != 1 || stats.Puts != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestStoreExpiresAndDropsStaleGeneration(t *testing.T) {
	store := newTestStore(t)
	store.ttl = 10 * time.Second
	now := time.Now()
	store.Put("/dir", entries(), 1, now)
	if _, ok := store.Get("/dir", now.Add(11*time.Second)); ok {
		t.Fatal("expired listing served")
	}
	if _, ok := store.Get("/dir", now); ok {
		t.Fatal("expired file not removed")
	}
}

func TestStoreIdentityMismatch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lists")
	if _, err := NewStore(dir, "m1", "old-account", time.Minute, 1<<20); err != nil {
		t.Fatal(err)
	}
	first, _ := NewStore(dir, "m1", "old-account", time.Minute, 1<<20)
	first.Put("/dir", entries(), 1, time.Now())
	second, _ := NewStore(dir, "m1", "new-account", time.Minute, 1<<20)
	if _, ok := second.Get("/dir", time.Now()); ok {
		t.Fatal("served listing from another identity")
	}
}

func TestStoreCorruptFileIsDropped(t *testing.T) {
	store := newTestStore(t)
	path := store.filePath("/dir")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("/dir", time.Now()); ok {
		t.Fatal("corrupt file served")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("corrupt file kept: %v", err)
	}
	if stats := store.Stats(); stats.CorruptDrops != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestStoreEvictsToLowWatermark(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lists")
	store, err := NewStore(dir, "m1", "id", time.Minute, 4096)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 20; i++ {
		store.Put("/dir-"+string(rune('a'+i)), []drive.Entry{{ID: "x", Name: "file.txt", Size: 1024}}, 1, now.Add(time.Duration(i)*time.Millisecond))
	}
	if stats := store.Stats(); stats.Bytes > 4096 {
		t.Fatalf("over cap: %+v", stats)
	}
	if stats := store.Stats(); stats.Evictions == 0 {
		t.Fatalf("no eviction: %+v", stats)
	}
}

func TestStoreClearAndDisabled(t *testing.T) {
	store := newTestStore(t)
	store.Put("/dir", entries(), 1, time.Now())
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("/dir", time.Now()); ok {
		t.Fatal("clear left a listing")
	}
	disabled, err := NewStore("", "m", "id", time.Minute, 0)
	if err != nil || disabled != nil {
		t.Fatalf("disabled store = %v %v", disabled, err)
	}
	var nilStore *Store
	nilStore.Put("/x", entries(), 1, time.Now())
	if _, ok := nilStore.Get("/x", time.Now()); ok {
		t.Fatal("nil store hit")
	}
	if nilStore.Enabled() {
		t.Fatal("nil store enabled")
	}
}
