package drive

import (
	"bytes"
	"context"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/readbudget"
)

func TestBodyProgressIgnoresReplayAndPreservesOffset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newPolicySource(t, bytes.Repeat([]byte("x"), 3*ReadChunkSize), 1)
		trace := &ReadTrace{}
		ctx := WithReadTrace(context.Background(), trace)
		e := Entry{ID: "0/file.bin", Size: 3 * ReadChunkSize}
		const start, size = 137, 32 << 10
		read := func(offset int64) {
			t.Helper()
			body, err := s.OpenRange(ctx, e, offset, size, readbudget.PriorityNormal, false)
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			if _, err := io.ReadAll(body); err != nil {
				t.Fatal(err)
			}
		}
		read(start)
		end, at := trace.BodyProgress()
		if end != start+size || at.IsZero() {
			t.Fatalf("wrong frontier: %d, %v", end, at)
		}
		time.Sleep(time.Second)
		read(start)
		if next, advanced := trace.BodyProgress(); next != end || advanced != at {
			t.Fatal("replayed bytes renewed progress")
		}
		read(start + size)
		if next, advanced := trace.BodyProgress(); next != end+size || !advanced.After(at) {
			t.Fatal("suffix did not advance progress")
		}
		if trace.Snapshot()["body_bytes"] != int64(3*size) {
			t.Fatal("replayed traffic disappeared from byte accounting")
		}
	})
}
