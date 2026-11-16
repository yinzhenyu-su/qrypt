package drive

import (
	"bytes"
	"context"
	"io"
	"testing"
)

// This byte contract is shared by every policy mode. Policy migration may
// change requests and scheduling, but not the result of an unaligned read.
func TestReadPolicyByteContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps []Capability
	}{
		{"single", nil}, {"parallel", []Capability{CapabilityParallelRange}},
		{"internal", []Capability{CapabilityInternalParallelRead}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := bytes.Repeat([]byte("boundary-content"), 70000)
			d := seededDriver(t, data, tc.caps...)
			entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
			for _, span := range []struct{ off, n int64 }{{0, 123}, {511999, 2001}, {int64(len(data)) - 17, 17}} {
				rc, err := readSourceDefault(context.Background(), d, entry, span.off, span.n)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(rc)
				closeErr := rc.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("read/close: %v/%v", err, closeErr)
				}
				if !bytes.Equal(got, data[span.off:span.off+span.n]) {
					t.Fatalf("wrong bytes at %d", span.off)
				}
			}
		})
	}
}
