package cassette

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Recorder wraps a drive.Driver and records its read traffic into a cassette.
// It records by interface embedding: every non-read method is forwarded to the
// wrapped driver unchanged, so only Read (the provider Range call) is observed
// and production behavior is untouched.
type Recorder struct {
	drive.Driver

	// Notes is free-form context stored with the cassette (incident link,
	// provider, date). Set it before calling Cassette.
	Notes string

	mu        sync.Mutex
	files     []FileRecord
	fileIndex map[string]struct{}
	attempts  map[requestKey]int
	active    int
	requests  []*RequestRecord
}

// NewRecorder returns a Recorder forwarding to d.
func NewRecorder(d drive.Driver) *Recorder {
	return &Recorder{
		Driver:    d,
		fileIndex: make(map[string]struct{}),
		attempts:  make(map[requestKey]int),
	}
}

// Read records one provider Range request and forwards it to the wrapped
// driver. The returned body is wrapped so body chunks and their delivery
// timing are recorded too.
func (r *Recorder) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	started := time.Now()
	key := requestKey{entryID: entry.ID, offset: offset, size: size}

	r.mu.Lock()
	r.attempts[key]++
	record := &RequestRecord{
		EntryID:    entry.ID,
		Offset:     offset,
		Size:       size,
		Attempt:    r.attempts[key],
		Concurrent: r.active + 1,
	}
	r.active++
	if _, ok := r.fileIndex[entry.ID]; !ok {
		r.fileIndex[entry.ID] = struct{}{}
		r.files = append(r.files, FileRecord{
			ID:           entry.ID,
			Name:         entry.Name,
			Size:         entry.Size,
			Capabilities: drive.Capabilities(r.Driver),
		})
	}
	r.mu.Unlock()

	body, err := r.Driver.Read(ctx, entry, offset, size)
	record.OpenNS = time.Since(started).Nanoseconds()
	if err != nil {
		record.ErrorKind, record.Status = classifyError(err)
		record.Error = err.Error()
		r.finish(record)
		return nil, err
	}
	return &recordBody{
		recorder: r,
		record:   record,
		body:     body,
		started:  time.Now(),
	}, nil
}

// Cassette returns a deep copy of everything recorded so far.
func (r *Recorder) Cassette() *Cassette {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := &Cassette{
		Version:  FormatVersion,
		Notes:    r.Notes,
		Files:    append([]FileRecord(nil), r.files...),
		Requests: make([]RequestRecord, len(r.requests)),
	}
	for i, record := range r.requests {
		clone := *record
		clone.Chunks = make([]ChunkRecord, len(record.Chunks))
		for j, chunk := range record.Chunks {
			clone.Chunks[j] = ChunkRecord{
				Data: append([]byte(nil), chunk.Data...),
				AtNS: chunk.AtNS,
			}
		}
		c.Requests[i] = clone
	}
	return c
}

// finish stores a completed request record and releases its concurrency slot.
func (r *Recorder) finish(record *RequestRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, record)
	r.active--
}

// recordBody wraps a driver body and records each chunk with its delivery time.
type recordBody struct {
	recorder *Recorder
	record   *RequestRecord
	body     io.ReadCloser
	started  time.Time
	pending  []byte
	firstAt  time.Time
	hasFirst bool
	total    int64
	done     bool
	err      error
}

func (b *recordBody) Read(p []byte) (int, error) {
	if b.done {
		return 0, b.err
	}
	n, err := b.body.Read(p)
	if n > 0 {
		if !b.hasFirst {
			b.hasFirst = true
			b.firstAt = time.Now()
			b.record.FirstByteNS = b.firstAt.Sub(b.started).Nanoseconds()
		}
		b.appendData(p[:n])
	}
	switch err {
	case nil:
		return n, nil
	case io.EOF:
		b.flush()
		b.record.CloseNS = time.Since(b.started).Nanoseconds()
		b.err = io.EOF
		b.finish()
		return n, io.EOF
	default:
		b.flush()
		b.record.BodyErrorKind, _ = classifyError(err)
		b.record.BodyError = err.Error()
		b.record.CloseNS = time.Since(b.started).Nanoseconds()
		b.err = err
		b.finish()
		return n, err
	}
}

func (b *recordBody) Close() error {
	if !b.done {
		b.flush()
		b.record.CloseNS = time.Since(b.started).Nanoseconds()
		// A body closed short of the requested range was cut off; a body closed
		// after delivering the whole range is just the caller stopping at its
		// limit (the buffered read path does exactly that).
		b.record.Incomplete = b.total < b.record.Size
		b.err = io.EOF
		if b.record.Incomplete {
			b.err = ErrIncompleteRecord
		}
		b.finish()
	}
	return b.body.Close()
}

// appendData adds bytes to the current chunk, flushing in ChunkSize pieces. It
// never records more than the requested range: a misbehaving wrapped driver
// must not make the cassette invalid.
func (b *recordBody) appendData(data []byte) {
	remaining := b.record.Size - b.total
	if remaining <= 0 {
		return
	}
	if int64(len(data)) > remaining {
		data = data[:remaining]
	}
	b.total += int64(len(data))
	b.pending = append(b.pending, data...)
	for len(b.pending) >= ChunkSize {
		b.flushChunk(b.pending[:ChunkSize])
		b.pending = b.pending[ChunkSize:]
	}
}

// flush emits the tail chunk, if any.
func (b *recordBody) flush() {
	if len(b.pending) == 0 {
		return
	}
	b.flushChunk(b.pending)
	b.pending = nil
}

// flushChunk appends one chunk stamped relative to the body hand-off. The
// chunk's time is the moment it became fully available to the caller.
func (b *recordBody) flushChunk(data []byte) {
	b.record.Chunks = append(b.record.Chunks, ChunkRecord{
		Data: append([]byte(nil), data...),
		AtNS: time.Since(b.started).Nanoseconds(),
	})
}

// finish marks the body done, stores its record and releases the concurrency
// slot. It is idempotent.
func (b *recordBody) finish() {
	if b.done {
		return
	}
	b.done = true
	b.recorder.finish(b.record)
}

var _ drive.Driver = (*Recorder)(nil)
