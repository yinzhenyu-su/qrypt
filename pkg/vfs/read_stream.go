package vfs

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

// StreamReader is the optional streaming surface for sequential downloads.
// ReadStream serves content in bounded read windows instead of buffering
// the whole file in memory like Read does.
type StreamReader interface {
	ReadStream(ctx context.Context, path string) (io.ReadCloser, error)
}

// ReadStream opens path for sequential streaming reads. Unlike Read, which
// reads the entire file into one buffer, ReadStream pulls chunk windows on
// demand so large downloads (fs sync, media) keep memory bounded by the
// read window size. Pending local changes are served from staging, matching
// Read. The returned closer is not safe for concurrent use.
func (v *VFS) ReadStream(ctx context.Context, path string) (io.ReadCloser, error) {
	var err error
	defer func() { v.recordHealthResult(drive.HealthOpRead, err) }()
	path = cleanVirtual(path)
	started := timeutil.Now()
	opID := v.nextDebugReadOpID()
	activeID := v.beginDebugActive(DebugActiveOp{
		OpID:   opID,
		Kind:   "vfs_read_stream",
		Phase:  "resolve",
		Path:   path,
		Offset: 0,
	})
	if pending, err := v.pendingUpload(path); err == nil {
		v.updateDebugActive(activeID, func(op *DebugActiveOp) {
			op.Phase = "staging_flush"
			op.RemoteID = pending.FID
		})
		if err := newVFSReadRuntime(v).FlushStaging(pending.LocalPath); err != nil {
			v.finishDebugActive(activeID)
			v.recordDebugRead(opID, path, pending.FID, 0, 0, 0, "staging", 0, 0, 0, started, nil, err)
			return nil, err
		}
		v.updateDebugActive(activeID, func(op *DebugActiveOp) {
			op.Phase = "staging_open"
		})
		rc, err := osutil.OpenRead(pending.LocalPath, 0, 0)
		if err != nil {
			v.finishDebugActive(activeID)
			v.recordDebugRead(opID, path, pending.FID, 0, 0, 0, "staging", 0, 0, 0, started, nil, err)
			return nil, err
		}
		return &debugReadCloser{ReadCloser: rc, finish: func(bytes int64, readErr error) {
			v.finishDebugActive(activeID)
			v.recordDebugRead(opID, path, pending.FID, 0, 0, bytes, "staging", 0, 0, 0, started, nil, readErr)
		}}, nil
	}
	entry, err := v.resolve(ctx, path)
	if err != nil {
		v.finishDebugActive(activeID)
		v.recordDebugRead(opID, path, "", 0, 0, 0, "remote", 0, 0, 0, started, nil, err)
		return nil, err
	}
	if entry.IsDir {
		err := fmt.Errorf("vfs: %s is a directory", path)
		v.finishDebugActive(activeID)
		v.recordDebugRead(opID, path, entry.ID, 0, 0, 0, "remote", 0, 0, 0, started, nil, err)
		return nil, err
	}
	hitsBefore, missesBefore := v.debugCacheCounters()
	return &chunkedStreamReader{
		ctx:          drive.WithDebugOperation(ctx, drive.DebugOperation{OpID: opID, Step: "vfs_read_stream", Name: path}),
		v:            v,
		path:         path,
		entry:        entry,
		opID:         opID,
		activeID:     activeID,
		started:      started,
		hitsBefore:   hitsBefore,
		missesBefore: missesBefore,
	}, nil
}

// chunkedStreamReader serves one file through readChunkRange, refilling one
// chunk at a time. The read window prefetch inside readChunkRange keeps the
// backing memory bounded by the window size rather than the file size.
type chunkedStreamReader struct {
	ctx       context.Context
	v         *VFS
	path      string
	entry     drive.Entry
	pos       int64
	buf       []byte
	bufPos    int
	bytesRead int64
	closed    bool

	// Debug bookkeeping finalized on Close.
	opID         string
	activeID     uint64
	started      time.Time
	hitsBefore   int64
	missesBefore int64
}

func (r *chunkedStreamReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.EOF
	}
	if r.bufPos >= len(r.buf) {
		if err := r.refill(); err != nil {
			return 0, err
		}
		if r.bufPos >= len(r.buf) {
			return 0, io.EOF
		}
	}
	n := copy(p, r.buf[r.bufPos:])
	r.bufPos += n
	r.pos += int64(n)
	r.bytesRead += int64(n)
	return n, nil
}

// refill loads the chunk containing r.pos. A short or empty chunk ends the
// stream (the file has no more data), matching readRange's stop condition.
func (r *chunkedStreamReader) refill() error {
	r.buf = nil
	r.bufPos = 0
	if r.entry.Size > 0 && r.pos >= r.entry.Size {
		return nil
	}
	chunkIndex := r.pos / readChunkSize
	start := r.pos - chunkIndex*readChunkSize
	want := readChunkSize - start
	if r.entry.Size > 0 && r.entry.Size-r.pos < want {
		want = r.entry.Size - r.pos
	}
	data, err := r.v.readChunkRange(r.ctx, r.entry, chunkIndex, start, want, readPrefetchChunks)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	r.buf = data
	return nil
}

func (r *chunkedStreamReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.finish(r.bytesRead, nil)
	return nil
}

func (r *chunkedStreamReader) finish(bytes int64, readErr error) {
	r.v.finishDebugActive(r.activeID)
	hitsAfter, missesAfter := r.v.debugCacheCounters()
	r.v.recordDebugRead(r.opID, r.path, r.entry.ID, 0, 0, bytes, "remote", hitsAfter-r.hitsBefore, missesAfter-r.missesBefore, 0, r.started, nil, readErr)
}
