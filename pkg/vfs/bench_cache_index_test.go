package vfs

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// BenchmarkCacheIndexContention isolates the in-memory index lock behavior:
// many readers doing HasChunk on different files while one writer keeps
// putting chunks (window load). This is where sharding should show gains —
// with a single global lock, the writer's exclusive lock blocks readers of
// unrelated files; with 16 shards they proceed in parallel.
func BenchmarkCacheIndexContention(b *testing.B) {
	const readers = 8
	c := &readCacheStore{}
	for i := range c.shards {
		c.shards[i].chunks = map[string]*fileChunks{}
	}
	// Seed: each reader file has one cached chunk.
	for r := 0; r < readers; r++ {
		fid := fmt.Sprintf("reader-%d", r)
		fc := c.fileChunks(fid)
		fc.chunks[0] = chunkInfo{file: "x", offset: 0, size: 1, accessAt: nowForTest()}
	}
	readerFIDs := make([]string, readers)
	for i := range readerFIDs {
		readerFIDs[i] = fmt.Sprintf("reader-%d", i)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		var n int
		for {
			select {
			case <-stop:
				return
			default:
			}
			fid := fmt.Sprintf("writer-%d", n%64)
			_ = c.fileChunks(fid)
			n++
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var gwg sync.WaitGroup
		for r := 0; r < readers; r++ {
			gwg.Add(1)
			go func(r int) {
				defer gwg.Done()
				if _, err := c.HasChunk(readerFIDs[r], int64(i%16)); err != nil {
					b.Error(err)
				}
			}(r)
		}
		gwg.Wait()
	}
	close(stop)
	wg.Wait()
}

func nowForTest() time.Time { return time.Now() }
