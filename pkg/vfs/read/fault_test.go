package read

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
)

type fakeReadFaults struct {
	fault faultinject.DebugReadFault
	ok    bool
}

func (f fakeReadFaults) MatchReadFault(string, int64, int64) (faultinject.DebugReadFault, bool) {
	return f.fault, f.ok
}

func (f fakeReadFaults) ConsumeReadFault(string) {}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestApplyReadFaultStatusReturnsClassifiedError(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader("data")}
	_, err := applyReadFault(context.Background(), fakeReadFaults{
		ok:    true,
		fault: faultinject.DebugReadFault{Kind: faultinject.ReadFaultStatus, Status: 503},
	}, drive.Entry{}, 0, 4, body)
	var status *drive.HTTPStatusError
	if !errors.As(err, &status) || status.Status != 503 {
		t.Fatalf("error = %v, want HTTP status 503", err)
	}
	if !body.closed {
		t.Fatal("status fault left the body open")
	}
}

func TestApplyReadFaultTruncateStopsAtAfterBytes(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader("hello world")}
	wrapped, err := applyReadFault(context.Background(), fakeReadFaults{
		ok:    true,
		fault: faultinject.DebugReadFault{Kind: faultinject.ReadFaultTruncate, AfterBytes: 5},
	}, drive.Entry{}, 0, 11, body)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("data = %q, want %q", data, "hello")
	}
}

func TestApplyReadFaultDelayHonorsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	body := &trackingReadCloser{Reader: strings.NewReader("data")}
	_, err := applyReadFault(ctx, fakeReadFaults{
		ok:    true,
		fault: faultinject.DebugReadFault{Kind: faultinject.ReadFaultDelay, DelayMS: 200},
	}, drive.Entry{}, 0, 4, body)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if !body.closed {
		t.Fatal("canceled delay left the body open")
	}
}

func TestApplyReadFaultStallWaitsForCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := &trackingReadCloser{Reader: strings.NewReader("hello world")}
	wrapped, err := applyReadFault(ctx, fakeReadFaults{
		ok:    true,
		fault: faultinject.DebugReadFault{Kind: faultinject.ReadFaultStall, AfterBytes: 5},
	}, drive.Entry{}, 0, 11, body)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 5)
	if _, err := io.ReadFull(wrapped, first); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := wrapped.Read(make([]byte, 1))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("stall returned before cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stall error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stall did not stop after cancellation")
	}
}
