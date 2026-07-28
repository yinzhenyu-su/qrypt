package task

import (
	"fmt"
	"path/filepath"
	"testing"
)

// Benchmarks for task progress update overhead. Every progress update in
// download/upload tasks goes through Store.UpdateManaged (two deep clones of
// the whole task incl. all item results) plus broadcastTaskUpdated (another
// clone + per-subscriber filter match). With no subscribers this is pure
// overhead.

func benchmarkTask(n int) Task {
	items := make([]ItemResult, n)
	for i := range items {
		items[i] = ItemResult{
			Path:            fmt.Sprintf("/remote/file-%d.bin", i),
			ItemID:          fmt.Sprintf("item-%d", i),
			SourcePath:      fmt.Sprintf("/local/file-%d.bin", i),
			State:           StateRunning,
			CloudBytesDone:  1 << 20,
			CloudBytesTotal: 1 << 20,
		}
	}
	return Task{
		ID:     "upload-bench",
		Type:   TypeUploadBatch,
		State:  StateRunning,
		Scope:  ScopeUser,
		Path:   "/remote",
		Name:   "batch",
		Detail: map[string]any{"phase": "upload", "concurrency": 4},
		Progress: Progress{
			ItemsTotal:      int64(n),
			CloudBytesTotal: int64(n) << 20,
		},
		Result: Result{Items: items},
	}
}

func BenchmarkUpdateManaged100Items(b *testing.B) {
	store := NewMemoryStore()
	item := benchmarkTask(100)
	store.PutManaged(ManagedTask{Task: item})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.UpdateManaged(item.ID, func(mt *ManagedTask) {
			mt.Task.Progress.CloudBytesDone += 1 << 20
		})
	}
}

func BenchmarkBroadcastTaskUpdatedNoSubscribers(b *testing.B) {
	m := NewManager()
	item := benchmarkTask(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.broadcastTaskUpdated(item)
	}
}

func BenchmarkBroadcastTaskUpdatedOneSubscriber(b *testing.B) {
	m := NewManager()
	sub := m.Subscribe(Filter{})
	defer sub.Close()
	item := benchmarkTask(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.broadcastTaskUpdated(item)
	}
}

// Combined per-update cost: what one progress tick pays today (manager.update).
func BenchmarkManagerUpdate100Items(b *testing.B) {
	m := NewManager()
	item := benchmarkTask(100)
	item.ID = "t"
	m.store.PutManaged(ManagedTask{Task: item})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.update("t", func(t *Task) {
			t.Progress.CloudBytesDone += 1 << 20
		})
	}
}

// Persistent batch tasks journal every progress update (json.Marshal + disk
// append). Crash recovery normalizes non-terminal states to failed, so the
// journaled progress is never used on replay — only terminal state matters.
func BenchmarkPersistentUpdate100Items(b *testing.B) {
	store, err := NewPersistentStore(filepath.Join(b.TempDir(), "tasks.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	item := benchmarkTask(100)
	item.Capabilities.Persistent = true
	store.PutManaged(ManagedTask{Task: item})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.UpdateManaged(item.ID, func(mt *ManagedTask) {
			mt.Task.Progress.CloudBytesDone += 1 << 20
		})
	}
}
