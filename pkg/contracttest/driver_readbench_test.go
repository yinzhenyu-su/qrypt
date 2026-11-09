package contracttest

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestParseConcurrencyList(t *testing.T) {
	levels, err := ParseConcurrencyList("4,1,2,4")
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 3 || levels[0] != 1 || levels[1] != 2 || levels[2] != 4 {
		t.Fatalf("levels = %v, want [1 2 4]", levels)
	}
	if _, err := ParseConcurrencyList("0"); err == nil {
		t.Fatal("zero concurrency accepted")
	}
	if _, err := ParseConcurrencyList("1,x"); err == nil {
		t.Fatal("non-numeric concurrency accepted")
	}
}

// rangeReadDriver honors offset and length so the benchmark's concurrent
// ranges are distinct; crudMemoryDriver always returns the whole file.
type rangeReadDriver struct {
	*crudMemoryDriver
	// delay keeps the read long enough to be observed by the platform clock.
	// Windows exposes a monotonic clock with millisecond granularity, so a
	// purely in-memory read would otherwise measure as zero duration.
	delay time.Duration
}

func (d *rangeReadDriver) Read(_ context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	data, ok := d.data[entry.ID]
	if !ok {
		return nil, drive.ErrNotFound
	}
	if offset > int64(len(data)) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	end := offset + size
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	if d.delay > 0 {
		time.Sleep(d.delay)
	}
	return io.NopCloser(bytes.NewReader(data[offset:end])), nil
}

func TestRunDriverReadBenchmark(t *testing.T) {
	driver := &rangeReadDriver{crudMemoryDriver: newCRUDMemoryDriver(), delay: 20 * time.Millisecond}
	report := RunDriverReadBenchmark(context.Background(), "mem", driver, 2<<20, []int{1, 2}, 1, 0)
	if report.Error != "" {
		t.Fatalf("benchmark error: %s", report.Error)
	}
	if len(report.Samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(report.Samples))
	}
	for _, sample := range report.Samples {
		if sample.Bytes != report.FileSize {
			t.Fatalf("sample %+v read %d bytes, want %d", sample, sample.Bytes, report.FileSize)
		}
		if sample.AggregateBPS <= 0 || sample.Errors != 0 || sample.Error != "" {
			t.Fatalf("sample %+v is not a clean measurement", sample)
		}
	}
	if report.CleanupError != "" {
		t.Fatalf("cleanup error: %s", report.CleanupError)
	}
}

func TestRunDriverReadBenchmarkRejectsOversize(t *testing.T) {
	report := RunDriverReadBenchmark(context.Background(), "mem", nil, MaxUploadProbeSize+1, []int{1}, 1, 0)
	if report.Error == "" {
		t.Fatal("oversize read benchmark accepted")
	}
}
