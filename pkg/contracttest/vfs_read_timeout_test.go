package contracttest

import (
	"os"
	"testing"
	"time"
)

// TestReadMountedCallTimesOutBlockedFile proves the probe's read-call bound
// returns a structured timeout instead of waiting for a stuck mount forever.
func TestReadMountedCallTimesOutBlockedFile(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	started := time.Now()
	n, timedOut, readErr := readMountedCall(reader, make([]byte, 16), 50*time.Millisecond)
	if !timedOut {
		t.Fatalf("readMountedCall did not time out (n=%d err=%v)", n, readErr)
	}
	if n != 0 || readErr != nil {
		t.Fatalf("timeout result = (%d, %v), want (0, nil)", n, readErr)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("read timeout took %s, want it bounded near 50ms", elapsed)
	}
}

func TestReadMountedFullTimesOutBlockedFile(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	n, timedOut, readErr := readMountedFull(reader, make([]byte, 16), 50*time.Millisecond)
	if !timedOut || n != 0 || readErr != nil {
		t.Fatalf("timeout result = (%d, %v, %v), want a bounded timeout", n, readErr, timedOut)
	}
}

func TestReadMountedCallReturnsData(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := writer.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 5)
	n, timedOut, readErr := readMountedCall(reader, buf, time.Second)
	if timedOut || readErr != nil || n != 5 {
		t.Fatalf("read = (%d, %v, timedOut=%v), want (5, nil, false)", n, readErr, timedOut)
	}
	if string(buf) != "hello" {
		t.Fatalf("data = %q, want hello", buf)
	}
}
