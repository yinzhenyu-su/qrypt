package contracttest

import (
	"context"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestRunDriverUploadTest(t *testing.T) {
	driver := newCRUDMemoryDriver()
	const size = 1 << 20
	result := RunDriverUploadTest(context.Background(), "mem", driver, size)
	if !result.Pass {
		t.Fatalf("upload test pass = false, steps=%#v residual=%#v", result.Steps, result.Residual)
	}
	if result.Upload == nil || len(result.Upload.Measurements) != 1 {
		t.Fatalf("upload measurements = %+v, want one", result.Upload)
	}
	measurement := result.Upload.Measurements[0]
	if measurement.Bytes != size || measurement.VerifyMode != "full" || measurement.VerifiedBytes != size {
		t.Fatalf("measurement = %+v, want full verification of %d bytes", measurement, size)
	}
	if measurement.ThroughputBPS <= 0 {
		t.Fatalf("throughput = %d, want positive", measurement.ThroughputBPS)
	}
	seen := map[string]bool{}
	for _, step := range result.Steps {
		seen[step.Operation] = true
	}
	for _, op := range []string{"mkdir", "put", "verify_put_list", "verify_content", "verify_cleanup_list"} {
		if !seen[op] {
			t.Fatalf("missing step %q, got %v", op, seen)
		}
	}
}

func TestRunDriverUploadTestRejectsOversize(t *testing.T) {
	result := RunDriverUploadTest(context.Background(), "mem", nil, MaxUploadProbeSize+1)
	if result.Pass {
		t.Fatal("oversize upload probe passed")
	}
	if len(result.Steps) != 1 || result.Steps[0].ErrorCategory != drive.ErrorCategoryInvalidRequest {
		t.Fatalf("steps = %#v, want one invalid-request step", result.Steps)
	}
}

func TestPatternFileSource(t *testing.T) {
	const size = int64(3 << 20)
	source := newPatternFileSource(size)
	if source.Size() != size {
		t.Fatalf("Size() = %d, want %d", source.Size(), size)
	}

	file, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	buf := make([]byte, 4)
	if _, err := file.ReadAt(buf, 100); err != nil {
		t.Fatal(err)
	}
	for i, got := range buf {
		if want := patternByte(100 + int64(i)); got != want {
			t.Fatalf("ReadAt(100) byte %d = %d, want %d", i, got, want)
		}
	}
	if _, err := file.Seek(size-2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 {
		t.Fatalf("tail read = %d bytes, want 2", len(rest))
	}

	sum, ok := source.Hash(drive.HashSHA256)
	if !ok {
		t.Fatal("Hash(sha256) unavailable")
	}
	hasher := sha256.New()
	chunk := make([]byte, 1<<20)
	for offset := int64(0); offset < size; {
		n := int64(len(chunk))
		if offset+n > size {
			n = size - offset
		}
		fillPattern(chunk[:n], offset)
		_, _ = hasher.Write(chunk[:n])
		offset += n
	}
	if want := hasher.Sum(nil); string(sum) != string(want) {
		t.Fatalf("sha256 = %x, want %x", sum, want)
	}
}

func TestSeededPatternFileSource(t *testing.T) {
	source := newSeededPatternFileSource(1<<20, 7)
	file, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	buf := make([]byte, 4)
	if _, err := file.ReadAt(buf, 100); err != nil {
		t.Fatal(err)
	}
	for i, got := range buf {
		if want := patternByte(107 + int64(i)); got != want {
			t.Fatalf("seeded byte %d = %d, want %d", i, got, want)
		}
	}
	// A different seed must produce different content for the same offset.
	other := newSeededPatternFileSource(1<<20, 8)
	otherFile, err := other.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer otherFile.Close()
	otherBuf := make([]byte, 4)
	if _, err := otherFile.ReadAt(otherBuf, 100); err != nil {
		t.Fatal(err)
	}
	if string(otherBuf) == string(buf) {
		t.Fatal("seeded sources produced identical content")
	}
}
