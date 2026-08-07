package vfs

import (
	"bytes"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/read"
	"testing"
)

const chunkCopyWindowSize = 4 << 20 // 4MB window = 4 chunks of 1MB

// BenchmarkFetchChunkPerChunkCopy models the current fetchChunkWindow
// behavior: one full data buffer, then make+copy per chunk.
func BenchmarkFetchChunkPerChunkCopy(b *testing.B) {
	data := bytes.Repeat([]byte{0xAB}, chunkCopyWindowSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		chunks := map[int64][]byte{}
		remaining := data
		for index := int64(0); len(remaining) > 0; index++ {
			chunkSize := read.ChunkSize
			if len(remaining) < chunkSize {
				chunkSize = len(remaining)
			}
			chunk := make([]byte, chunkSize)
			copy(chunk, remaining[:chunkSize])
			chunks[index] = chunk
			remaining = remaining[chunkSize:]
		}
		if len(chunks) != 4 {
			b.Fatal("bad chunk count")
		}
	}
}

// BenchmarkFetchChunkSharedBacking models the alternative: slice the single
// data buffer directly (zero make+copy), accepting shared backing arrays.
func BenchmarkFetchChunkSharedBacking(b *testing.B) {
	data := bytes.Repeat([]byte{0xAB}, chunkCopyWindowSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		chunks := map[int64][]byte{}
		remaining := data
		for index := int64(0); len(remaining) > 0; index++ {
			chunkSize := read.ChunkSize
			if len(remaining) < chunkSize {
				chunkSize = len(remaining)
			}
			chunks[index] = remaining[:chunkSize]
			remaining = remaining[chunkSize:]
		}
		if len(chunks) != 4 {
			b.Fatal("bad chunk count")
		}
	}
}

// BenchmarkFetchChunkPreallocChunks models a middle ground: allocate the
// chunks map only (the per-chunk slices share one read buffer).
func BenchmarkFetchChunkPreallocMap(b *testing.B) {
	data := bytes.Repeat([]byte{0xAB}, chunkCopyWindowSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		chunks := make(map[int64][]byte, 4)
		remaining := data
		for index := int64(0); len(remaining) > 0; index++ {
			chunkSize := read.ChunkSize
			if len(remaining) < chunkSize {
				chunkSize = len(remaining)
			}
			chunk := make([]byte, chunkSize)
			copy(chunk, remaining[:chunkSize])
			chunks[index] = chunk
			remaining = remaining[chunkSize:]
		}
		if len(chunks) != 4 {
			b.Fatal("bad chunk count")
		}
	}
}
