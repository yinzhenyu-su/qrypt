package mount

import (
	"context"
	"sync"
	"testing"
)

// BenchmarkFuseBeginOpEnd measures the per-op fixed cost of adapter.beginOp
// (mutex + map insert/delete + time.Now()) that every FUSE op pays.
func BenchmarkFuseBeginOpEnd(b *testing.B) {
	a := &adapter{
		ctx: context.Background(),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, done, ok := a.beginOp("Getattr", "/a.txt")
		if !ok {
			b.Fatal("not ok")
		}
		done()
	}
}

// BenchmarkFuseBeginOpEndParallel measures the same under contention, which is
// the realistic FUSE pattern (many threads issuing ops concurrently).
func BenchmarkFuseBeginOpEndParallel(b *testing.B) {
	a := &adapter{
		ctx: context.Background(),
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, done, ok := a.beginOp("Getattr", "/a.txt")
			if !ok {
				b.Fatal("not ok")
			}
			done()
		}
	})
}

// BenchmarkLockedMapOps isolates the mutex+map cost (what beginOp does) to
// show the pure synchronization floor.
func BenchmarkLockedMapOps(b *testing.B) {
	var mu sync.Mutex
	m := map[uint64]int{}
	var seq uint64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mu.Lock()
		seq++
		id := seq
		m[id] = 1
		mu.Unlock()
		mu.Lock()
		delete(m, id)
		mu.Unlock()
	}
}
