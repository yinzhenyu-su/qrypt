package read

import (
	"bytes"
	"context"
	"testing"
	"time"
)

type emittedBlock struct {
	offset int64
	data   []byte
}

func blockOffsets(blocks []emittedBlock) []int64 {
	offsets := make([]int64, len(blocks))
	for i, block := range blocks {
		offsets[i] = block.offset
	}
	return offsets
}

func collectBlocks(dst *[]emittedBlock) func(int64, []byte) {
	return func(offset int64, block []byte) {
		*dst = append(*dst, emittedBlock{offset: offset, data: append([]byte(nil), block...)})
	}
}

// TestWindowBlocksReassemblesOutOfOrderParts: parts complete in any order (and
// may end mid-block); the joiner must publish whole blocks in offset order.
func TestWindowBlocksReassemblesOutOfOrderParts(t *testing.T) {
	var got []emittedBlock
	joiner := newWindowBlocks(0, collectBlocks(&got))
	piece := func(n int64) []byte { return bytes.Repeat([]byte("x"), int(n)) }
	joiner.put(2*ChunkSize, piece(ChunkSize)) // arrives first
	joiner.put(0, piece(ChunkSize/2))
	joiner.put(ChunkSize, piece(ChunkSize))
	if len(got) != 0 {
		t.Fatalf("published %d blocks before the first chunk was contiguous", len(got))
	}
	joiner.put(ChunkSize/2, piece(ChunkSize/2))
	if len(got) != 3 || got[0].offset != 0 || got[1].offset != ChunkSize || got[2].offset != 2*ChunkSize {
		t.Fatalf("published offsets = %v, want 0, %d, %d", blockOffsets(got), ChunkSize, 2*ChunkSize)
	}
	joiner.put(3*ChunkSize, piece(1024))
	joiner.finish()
	if len(got) != 4 || got[3].offset != 3*ChunkSize {
		t.Fatalf("published offsets = %v, want a tail block at %d", blockOffsets(got), 3*ChunkSize)
	}
	if len(got[3].data) != 1024 {
		t.Fatalf("tail block size = %d, want 1024", len(got[3].data))
	}
	if end := joiner.publishedEnd(); end != 3*ChunkSize+1024 {
		t.Fatalf("publishedEnd = %d, want %d", end, 3*ChunkSize+1024)
	}
}

// TestStreamWindowPartsPublishesBlocksInOrder: the read layer schedules the
// window's parts itself and every published block is chunk aligned and in
// order.
func TestStreamWindowPartsPublishesBlocksInOrder(t *testing.T) {
	sr := newStagedReader(t, 4*int(ChunkSize))
	w := windowFetch{
		ctx:         context.Background(),
		entry:       sr.host.entry,
		windowBytes: 2 * ChunkSize,
		streams:     2,
		partBytes:   ChunkSize,
		started:     time.Now(),
	}
	load := &windowLoad{
		start: 0,
		end:   1,
		chunks: []windowChunk{
			{ready: make(chan struct{})},
			{ready: make(chan struct{})},
		},
		data:        map[int64][]byte{},
		progressive: true,
	}
	delivered, err := sr.reader.streamWindowParts(w.ctx, sr.host.entry, w, 0, load, sr.reader.newBackend())
	if err != nil {
		t.Fatalf("streamWindowParts: %v", err)
	}
	if delivered != 2*ChunkSize {
		t.Fatalf("delivered = %d, want %d", delivered, 2*ChunkSize)
	}
	want := sr.host.data[:2*ChunkSize]
	if !bytes.Equal(load.chunks[0].data, want[:ChunkSize]) || !bytes.Equal(load.chunks[1].data, want[ChunkSize:]) {
		t.Fatal("published window data differs from the driver content")
	}
}
