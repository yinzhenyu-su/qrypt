package read

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// StreamReader is the optional streaming surface for sequential downloads.
// ReadStream serves content in bounded read windows instead of buffering
// the whole file in memory like Read does.
type StreamReader interface {
	ReadStream(ctx context.Context, path string) (io.ReadCloser, error)
}

// ContextReader is implemented by bounded sequential streams that can apply
// a context to the next refill. It lets mobile cancel one blocked read while
// keeping the stream handle usable.
type ContextReader interface {
	ReadContext(context.Context, []byte) (int, error)
}

type debugReadCloser struct {
	io.ReadCloser
	finish func(int64, error)
	// once guards finish so a double Close (legal on io.ReadCloser, and
	// common with defer + explicit close) reports the active operation
	// exactly once to the observer.
	once sync.Once
}

func (d *debugReadCloser) Close() error {
	err := d.ReadCloser.Close()
	d.once.Do(func() { d.finish(0, err) })
	return err
}

func (d *debugReadCloser) ReadContext(ctx context.Context, p []byte) (int, error) {
	if reader, ok := d.ReadCloser.(ContextReader); ok {
		return reader.ReadContext(ctx, p)
	}
	return d.Read(p)
}

// ReadStream opens path for sequential streaming reads. Unlike Read, which
// reads the requested range into one buffer, ReadStream pulls one window at a
// time through the same Read engine so large downloads (fs sync, media) keep
// memory bounded by the read window size. Pending local changes are served
// from staging, matching Read. The returned closer is not safe for concurrent
// use.
func (r *Reader) ReadStream(ctx context.Context, path string) (io.ReadCloser, error) {
	var err error
	defer func() { r.health.RecordResult(drive.HealthOpRead, err) }()
	path = CleanVirtualPath(path)
	started := util.Now()
	opID := r.observer.DebugNextOpID()
	activeID := r.observer.DebugBeginActive(vfstypes.DebugActiveOp{
		OpID:   opID,
		Kind:   "vfs_read_stream",
		Phase:  "resolve",
		Path:   path,
		Offset: 0,
	})
	if pending, ok, err := r.pendingUpload(path); err == nil && ok {
		r.observer.DebugUpdateActive(activeID, func(op *vfstypes.DebugActiveOp) {
			op.Phase = "staging_open"
			op.RemoteID = pending.FID
		})
		rc, err := util.OpenRead(pending.LocalPath, 0, 0)
		if err != nil {
			r.observer.DebugFinishActive(activeID)
			r.observer.DebugRecordRead(opID, path, pending.FID, 0, 0, 0, "staging", 0, 0, 0, started, nil, err)
			return nil, err
		}
		return &debugReadCloser{ReadCloser: rc, finish: func(bytes int64, readErr error) {
			r.observer.DebugFinishActive(activeID)
			r.observer.DebugRecordRead(opID, path, pending.FID, 0, 0, bytes, "staging", 0, 0, 0, started, nil, readErr)
		}}, nil
	}
	entry, err := r.resolve(ctx, path)
	if err != nil {
		r.observer.DebugFinishActive(activeID)
		r.observer.DebugRecordRead(opID, path, "", 0, 0, 0, "remote", 0, 0, 0, started, nil, err)
		return nil, err
	}
	if entry.IsDir {
		err := fmt.Errorf("vfs: %s is a directory", path)
		r.observer.DebugFinishActive(activeID)
		r.observer.DebugRecordRead(opID, path, entry.ID, 0, 0, 0, "remote", 0, 0, 0, started, nil, err)
		return nil, err
	}
	hitsBefore, missesBefore := r.observer.DebugCacheCounters()
	lifetime, cancel := context.WithCancel(ctx)
	return &chunkedStreamReader{
		ctx:          lifetime,
		cancel:       cancel,
		scope:        &ReadScope{ctx: lifetime, cancel: cancel},
		reader:       r,
		path:         path,
		entry:        entry,
		opID:         opID,
		activeID:     activeID,
		started:      started,
		hitsBefore:   hitsBefore,
		missesBefore: missesBefore,
	}, nil
}

// chunkedStreamReader serves one file by reading one window at a time through
// the ordinary Read path. The stream keeps a persistent scope so the lookahead
// scheduled by each refill survives until the handle is closed, and it marks
// its refills silent because the stream reports one read record on Close.
type chunkedStreamReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	scope  *ReadScope
	reader *Reader
	path   string
	entry  drive.Entry
	pos    int64

	buf    []byte
	bufPos int

	bytesRead int64
	closed    bool
	// readErr is the first error the stream surfaced, recorded with the
	// summary event on Close so a timed-out refill is visible as a failure.
	readErr error

	// Debug bookkeeping finalized on Close.
	opID         string
	activeID     uint64
	started      time.Time
	hitsBefore   int64
	missesBefore int64
}

func (r *chunkedStreamReader) Read(p []byte) (int, error) {
	return r.ReadContext(r.ctx, p)
}

func (r *chunkedStreamReader) ReadContext(ctx context.Context, p []byte) (int, error) {
	if r.closed {
		return 0, io.EOF
	}
	if r.bufPos >= len(r.buf) {
		if err := r.refill(ctx); err != nil {
			if r.readErr == nil {
				r.readErr = err
			}
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

// refill loads the next window through the shared Read path. The window size
// comes from the same plan the read path uses, so a streaming download fetches
// the same Ranges an equivalent one-shot read would.
func (r *chunkedStreamReader) refill(ctx context.Context) error {
	r.buf, r.bufPos = nil, 0
	if r.entry.Size > 0 && r.pos >= r.entry.Size {
		return nil
	}
	stage := StageInitial
	if r.pos > 0 {
		stage = StageSequential
	}
	plan := r.reader.resolvePlan(r.ctx, PrefetchChunks, stage)
	want := int64(max(1, plan.WindowChunks)) * ChunkSize
	if r.entry.Size > 0 && r.entry.Size-r.pos < want {
		want = r.entry.Size - r.pos
	}
	readCtx := withSilentReadEvents(context.WithValue(ctx, streamScopeKey{}, r.scope))
	rc, err := r.reader.Read(readCtx, r.path, r.pos, want)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	r.buf = data
	return nil
}

func (r *chunkedStreamReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.scope.cancel()
	r.scope.jobs.Wait()
	r.cancel()
	r.finish(r.bytesRead, nil)
	return nil
}

func (r *chunkedStreamReader) finish(bytes int64, readErr error) {
	if readErr == nil {
		readErr = r.readErr
	}
	r.reader.observer.DebugFinishActive(r.activeID)
	hitsAfter, missesAfter := r.reader.observer.DebugCacheCounters()
	r.reader.observer.DebugRecordRead(r.opID, r.path, r.entry.ID, 0, 0, bytes, "remote", hitsAfter-r.hitsBefore, missesAfter-r.missesBefore, 0, r.started, ReadEventExtra(r.ctx, 0, readErr), readErr)
}
