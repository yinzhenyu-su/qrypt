package readcache

import (
	"testing"
)

// Warm-cache chunk read cost. After a file's chunks are in the disk cache,
// every sequential re-read (repeat download, FUSE read) pays os.Open + ReadAt
// + a fresh 1MB allocation per chunk.

func benchWarmReadCache(b *testing.B, chunks int) *Store {
	dir := b.TempDir()
	store, err := NewStore(dir, 256<<20)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	fid := "warm-fid"
	data := make([]byte, readChunkSize)
	for i := range chunks {
		if err := store.PutChunk(fid, int64(chunks)*readChunkSize, int64(i), data); err != nil {
			b.Fatal(err)
		}
	}
	return store
}

func BenchmarkReadCacheGetChunkRange1(b *testing.B)   { benchGetChunkRange(b, 1) }
func BenchmarkReadCacheGetChunkRange16(b *testing.B)  { benchGetChunkRange(b, 16) }
func BenchmarkReadCacheGetChunkRange256(b *testing.B) { benchGetChunkRange(b, 256) }

func benchGetChunkRange(b *testing.B, chunks int) {
	store := benchWarmReadCache(b, chunks)
	fid := "warm-fid"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := int64(i % chunks)
		if _, ok, err := store.GetChunkRange(fid, index, 0, readChunkSize); err != nil {
			b.Fatal(err)
		} else if !ok {
			b.Fatal("chunk not found")
		}
	}
}
