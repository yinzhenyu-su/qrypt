package vfs

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

// BenchmarkCacheConcurrentMixedReads simulates the FUSE pattern: many
// goroutines reading cached chunks of different files while one goroutine
// keeps writing new chunks (window load). With sharded locks, readers of one
// file must not block on writers of another.
func BenchmarkCacheConcurrentMixedReads(b *testing.B) {
	const files = 64
	const readers = 8
	const windowBytes = readChunkSize * 4

	fs, err := New(localfs.New(b.TempDir()), Options{StorageDir: b.TempDir()})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	data := make([]byte, windowBytes)
	for i := range data {
		data[i] = byte(i)
	}
	var writePath []string
	for i := 0; i < files; i++ {
		p := fmt.Sprintf("/file-%d.bin", i)
		if err := fs.Create(ctx, p); err != nil {
			b.Fatal(err)
		}
		writePath = append(writePath, p)
	}
	// Seed the cache: read each file once so chunks land in the read cache.
	for _, p := range writePath {
		rc, err := fs.Read(ctx, p, 0, windowBytes)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = readAllLimited(rc, windowBytes)
		_ = rc.Close()
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var writerErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		var tick int
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Simulate a window load: write a fresh file so putChunk +
			// eviction run against a different shard than the readers.
			p := fmt.Sprintf("/writer-%d.bin", tick)
			if err := fs.Create(ctx, p); err != nil {
				writerErr = err
				return
			}
			rc, err := fs.Read(ctx, p, 0, windowBytes)
			if err != nil {
				writerErr = err
				return
			}
			_, _ = readAllLimited(rc, windowBytes)
			_ = rc.Close()
			tick++
			time.Sleep(time.Microsecond * 50)
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
				p := writePath[(i+r)%files]
				rc, err := fs.Read(ctx, p, 0, readChunkSize)
				if err != nil {
					return
				}
				_, _ = readAllLimited(rc, readChunkSize)
				_ = rc.Close()
			}(r)
		}
		gwg.Wait()
	}
	close(stop)
	wg.Wait()
	if writerErr != nil {
		b.Fatal(writerErr)
	}
}
