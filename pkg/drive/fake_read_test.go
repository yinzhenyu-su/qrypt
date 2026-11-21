package drive

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func seedFakeReadFile(t *testing.T, d *FakeDriver, size int) Entry {
	t.Helper()
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i)
	}
	if err := d.Seed(map[string]string{"file.bin": string(content)}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	return Entry{ID: "0/file.bin", Name: "file.bin", Size: int64(size), ModTime: time.Unix(1700000000, 0)}
}

func TestFakeDriverReadRecordsConcurrency(t *testing.T) {
	d := NewFakeDriver()
	// Park every read on the gate: the assertion is about reads being in
	// flight together, so the test must create that state instead of hoping
	// that a wall-clock delay covers the goroutine scheduling it depends on.
	const readers = 4
	gate := make(chan struct{}, readers)
	d.Gate = gate
	entry := seedFakeReadFile(t, d, 4<<20)

	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rc, err := d.Read(context.Background(), entry, int64(i*(1<<20)), 1<<20)
			if err != nil {
				errs <- err
				return
			}
			defer rc.Close()
			_, err = io.Copy(io.Discard, rc)
			errs <- err
		}(i)
	}
	deadline := time.Now().Add(5 * time.Second)
	for d.ReadStats().Active < readers && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Release whatever is parked, including on the timeout path, so no
	// goroutine outlives the test.
	for i := 0; i < readers; i++ {
		gate <- struct{}{}
	}
	if active := d.ReadStats().Active; active < readers {
		t.Fatalf("only %d of %d reads reached the driver", active, readers)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}

	stats := d.ReadStats()
	if stats.Calls != readers {
		t.Fatalf("calls = %d, want %d", stats.Calls, readers)
	}
	if stats.MaxConcurrent < readers {
		t.Fatalf("max concurrent = %d, want at least %d", stats.MaxConcurrent, readers)
	}
	if stats.Active != 0 {
		t.Fatalf("active after completion = %d, want 0", stats.Active)
	}
}

func TestFakeDriverReadIgnoreRangeStrict(t *testing.T) {
	d := NewFakeDriver()
	d.ReadBehavior.IgnoreRange = true
	entry := seedFakeReadFile(t, d, 1<<20)

	if _, err := d.Read(WithStrictRange(context.Background()), entry, 512<<10, 1024); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("strict read error = %v, want ErrUnsupported", err)
	}
	rc, err := d.Read(context.Background(), entry, 512<<10, 1024)
	if err != nil {
		t.Fatalf("non-strict read: %v", err)
	}
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	rc.Close()
	if len(data) != 1024 {
		t.Fatalf("requested span length = %d, want 1024", len(data))
	}
	stats := d.ReadStats()
	if stats.FullBody != 1 {
		t.Fatalf("full body calls = %d, want 1", stats.FullBody)
	}
	calls := d.ReadCalls()
	if len(calls) != 2 || !calls[0].Strict || calls[1].Strict {
		t.Fatalf("read calls = %+v, want strict probe then non-strict read", calls)
	}
	if calls[1].Served != 1<<20 {
		t.Fatalf("served = %d, want full object %d", calls[1].Served, 1<<20)
	}
}
