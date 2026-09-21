package crypt

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"sync"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// countingReadSource wraps a source and counts how many plaintext bytes its
// handles actually read, so tests can assert on read amplification.
type countingReadSource struct {
	source drive.ReadOnlyFileSource

	mu    sync.Mutex
	read  int64
	opens int
}

func newCountingReadSource(data []byte) *countingReadSource {
	return &countingReadSource{source: newBytesReadOnlyFileSource(data)}
}

func (s *countingReadSource) Size() int64 { return s.source.Size() }

func (s *countingReadSource) Open(ctx context.Context) (drive.ReadOnlyFile, error) {
	f, err := s.source.Open(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.opens++
	s.mu.Unlock()
	return &countingReadFile{ReadOnlyFile: f, source: s}, nil
}

func (s *countingReadSource) bytesRead() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read
}

func (s *countingReadSource) note(n int) {
	s.mu.Lock()
	s.read += int64(n)
	s.mu.Unlock()
}

type countingReadFile struct {
	drive.ReadOnlyFile
	source *countingReadSource
}

func (f *countingReadFile) Read(p []byte) (int, error) {
	n, err := f.ReadOnlyFile.Read(p)
	f.source.note(n)
	return n, err
}

func (f *countingReadFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := f.ReadOnlyFile.ReadAt(p, off)
	f.source.note(n)
	return n, err
}

func testEncryptedSource(t *testing.T, plain []byte) (*countingReadSource, Cipher, [FileNonceSize]byte) {
	t.Helper()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	var nonce [FileNonceSize]byte
	copy(nonce[:], []byte("read-amplification-nonce"))
	return newCountingReadSource(plain), cp, nonce
}

func testPlaintext(size int) []byte {
	plain := make([]byte, size)
	for i := range plain {
		plain[i] = byte(i % 251)
	}
	return plain
}

// TestEncryptedReadOnlyFileSourceReadsEachBlockOnce pins the read bound of the
// upload source: a block can only be encrypted whole, but consumers read it in
// windows far smaller than BlockDataSize, so without a block memo every window
// re-reads its whole 64 KiB block (measured 17x at 4 KiB reads, 3x at the
// 32 KiB reads io.Copy and the HTTP transport use).
func TestEncryptedReadOnlyFileSourceReadsEachBlockOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Keep the fixture small: the amplification factor is what matters, and
	// the race layer runs this alongside every other package's tests.
	plain := testPlaintext(BlockDataSize*4 + 123)
	for _, chunkSize := range []int{4 << 10, 32 << 10, 64 << 10} {
		src, cp, nonce := testEncryptedSource(t, plain)
		enc := newEncryptedReadOnlyFileSource(src, cp, nonce, nil)
		f, err := enc.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, chunkSize)
		var delivered int64
		for {
			n, err := f.Read(buf)
			delivered += int64(n)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("chunk %d: %v", chunkSize, err)
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		want, err := io.ReadAll(NewEncryptingReader(bytes.NewReader(plain), cp, nonce, int64(len(plain))))
		if err != nil {
			t.Fatal(err)
		}
		if delivered != int64(len(want)) {
			t.Fatalf("chunk %d: delivered %d encrypted bytes, want %d", chunkSize, delivered, len(want))
		}
		// One extra boundary block per read window is the most the memo still
		// allows; a window-aligned reader stays at 1.0x.
		limit := int64(float64(len(plain))*1.1) + int64(chunkSize)
		if read := src.bytesRead(); read > limit {
			t.Fatalf("chunk %d: read %d plaintext bytes for a %d byte source (limit %d)", chunkSize, read, len(plain), limit)
		}
	}
}

// TestEncryptedReadOnlyFileSourceReadAtReusesMemoizedBlock covers the access
// pattern multipart drivers use: ranged reads that walk forward across block
// boundaries, jump back into an already encrypted block, and touch the partial
// tail block. Every read must match the reference encrypting reader.
func TestEncryptedReadOnlyFileSourceReadAtReusesMemoizedBlock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	plain := testPlaintext(BlockDataSize*3 + 77)
	src, cp, nonce := testEncryptedSource(t, plain)
	enc := newEncryptedReadOnlyFileSource(src, cp, nonce, nil)
	f, err := enc.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want, err := io.ReadAll(NewEncryptingReader(bytes.NewReader(plain), cp, nonce, int64(len(plain))))
	if err != nil {
		t.Fatal(err)
	}
	total := int64(len(want))

	// Forward walk in 3 KiB steps that straddle block boundaries.
	for off, step := int64(0), int64(3<<10); off < total; off, step = off+step, step+1 {
		assertReadAtMatches(t, f, want, off, 5<<10)
	}
	// Backwards jumps into blocks the memo may still hold.
	for _, off := range []int64{0, int64(FileHeaderSize), int64(FileHeaderSize + BlockSize - 1), BlockSize * 2, total - 1, total - 9} {
		if off < 0 {
			continue
		}
		assertReadAtMatches(t, f, want, off, 256)
	}
	// Random reads, including partial tail-block probes.
	rng := rand.New(rand.NewSource(7))
	for range 80 {
		off := rng.Int63n(total)
		size := min(int64(1+rng.Intn(8<<10)), total-off)
		assertReadAtMatches(t, f, want, off, size)
	}
	// The whole file still decodes through the reference reader.
	all, err := io.ReadAll(io.NewSectionReader(f, 0, total))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(all, want) {
		t.Fatal("whole-file read differs from reference encrypting reader")
	}
}

func assertReadAtMatches(t *testing.T, f drive.ReadOnlyFile, want []byte, off, size int64) {
	t.Helper()
	if off >= int64(len(want)) {
		return
	}
	if off+size > int64(len(want)) {
		size = int64(len(want)) - off
	}
	buf := make([]byte, size)
	n, err := f.ReadAt(buf, off)
	if err != nil && (err != io.EOF || n != len(buf)) {
		t.Fatalf("ReadAt(off=%d size=%d): %v", off, size, err)
	}
	if n != len(buf) {
		t.Fatalf("ReadAt(off=%d size=%d) = %d bytes, want %d", off, size, n, len(buf))
	}
	if !bytes.Equal(buf, want[off:off+size]) {
		t.Fatalf("ReadAt(off=%d size=%d) returned wrong encrypted bytes", off, size)
	}
}
