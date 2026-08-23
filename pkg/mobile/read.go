package mobile

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	vfsread "github.com/yinzhenyu/qrypt/pkg/vfs/read"
)

type openFileOptions struct {
	DeadlineMS int    `json:"deadline_ms"`
	Priority   string `json:"priority"`
}

func openFileWithPriority(coreID, path string, priority vfs.ReadPriority, deadlineMS int) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	item, err := withCore(s, func(c *core.Core) (drive.Entry, error) { return c.Stat(ctx, path) })
	if err != nil {
		return "", wrapError(err)
	}
	if item.IsDir {
		return "", wrapError(fmt.Errorf("mobile: %s is a directory", path))
	}
	id, err := newID()
	if err != nil {
		return "", wrapError(err)
	}
	registry.mu.Lock()
	registry.files[id] = &fileHandle{
		coreID:       coreID,
		path:         path,
		size:         item.Size,
		readPriority: priority,
		readSession:  nextReadSessionID(),
	}
	registry.mu.Unlock()
	return id, nil
}

func OpenFileJSON(coreID, path, optionsRaw string) string {
	options, err := parseOpenFileOptions(optionsRaw)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	priority, err := options.priority()
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	id, err := openFileWithPriority(coreID, path, priority, options.DeadlineMS)
	return resultJSON(id, err)
}

func parseOpenFileOptions(raw string) (openFileOptions, error) {
	if raw == "" {
		return openFileOptions{}, nil
	}
	var options openFileOptions
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return openFileOptions{}, err
	}
	return options, nil
}

func (o openFileOptions) priority() (vfs.ReadPriority, error) {
	switch o.Priority {
	case "", "normal":
		return vfs.PriorityNormal, nil
	case "high":
		return vfs.PriorityHigh, nil
	default:
		return vfs.PriorityNormal, fmt.Errorf("mobile: unknown file priority %q", o.Priority)
	}
}

func ReadAtInto(handleID string, offset int64, dst []byte, deadlineMS int) (int, error) {
	n, err := readAtInto(handleID, 0, offset, dst, deadlineMS, false)
	if err != nil {
		return n, wrapError(err)
	}
	return n, nil
}

// ReadAtIntoWithRequest is ReadAtInto with a caller-supplied request ID.
// The ID can be passed to CancelFileReadRequestJSON to cancel only this read.
// A zero ID allocates one internally and has the same behavior as ReadAtInto.
func ReadAtIntoWithRequest(handleID string, requestID int64, offset int64, dst []byte, deadlineMS int) (int, error) {
	if requestID < 0 {
		return 0, wrapError(fmt.Errorf("mobile: request ID must be non-negative"))
	}
	n, err := readAtInto(handleID, uint64(requestID), offset, dst, deadlineMS, false)
	if err != nil {
		return n, wrapError(err)
	}
	return n, nil
}

func readAtInto(handleID string, requestID uint64, offset int64, dst []byte, deadlineMS int, forceConcurrent bool) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("mobile: offset must be non-negative")
	}
	if len(dst) == 0 {
		return 0, nil
	}
	handle, err := getFile(handleID)
	if err != nil {
		return 0, err
	}
	if handle.stream != nil {
		return 0, fmt.Errorf("mobile: file handle is a sequential stream")
	}
	s, err := getSession(handle.coreID)
	if err != nil {
		return 0, err
	}
	ctx, done, concurrent, accessID, _, err := handle.reads.beginRequest(deadlineMS, requestID)
	if err != nil {
		return 0, err
	}
	defer done()
	ctx = vfs.WithReadPriority(ctx, handle.readPriority)
	ctx = vfsread.WithAccessHint(ctx, vfsread.AccessHint{
		SessionID:  handle.readSession,
		RequestID:  accessID,
		Concurrent: concurrent || forceConcurrent,
	})
	n, err := withCore(s, func(c *core.Core) (int, error) { return c.ReadAtInto(ctx, handle.path, offset, dst, 0) })
	return n, err
}

// ReadAtBatchInto reads fixed-size slots in one mobile boundary call. Each
// offset maps to dst[i*slotSize:(i+1)*slotSize]. Reads run concurrently so a
// batch of independent media ranges does not serialize on JNI round trips.
func readAtBatchInto(handleID string, offsets []int64, dst []byte, slotSize int, deadlineMS int) ([]int, error) {
	if len(offsets) == 0 {
		return []int{}, nil
	}
	if slotSize <= 0 {
		return nil, wrapError(fmt.Errorf("mobile: batch slot size must be positive"))
	}
	if len(offsets) > 64 || slotSize > len(dst)/len(offsets) {
		return nil, wrapError(fmt.Errorf("mobile: invalid batch buffer: %d offsets, slot size %d, buffer %d", len(offsets), slotSize, len(dst)))
	}
	counts := make([]int, len(offsets))
	errs := make([]error, len(offsets))
	var wg sync.WaitGroup
	for i, offset := range offsets {
		i, offset := i, offset
		wg.Add(1)
		go func() {
			defer wg.Done()
			counts[i], errs[i] = readAtInto(handleID, 0, offset, dst[i*slotSize:(i+1)*slotSize], deadlineMS, true)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return counts, wrapError(err)
		}
	}
	return counts, nil
}

// ReadAtBatchIntoJSON reads fixed-size slots described by a JSON int64 array
// and returns the per-slot byte counts in the normal JSON envelope. The bytes
// are written to dst in slot order.
func ReadAtBatchIntoJSON(handleID string, offsetsRaw string, dst []byte, slotSize int, deadlineMS int) string {
	var offsets []int64
	if err := json.Unmarshal([]byte(offsetsRaw), &offsets); err != nil {
		return resultJSON(nil, wrapError(fmt.Errorf("mobile: invalid batch offsets: %w", err)))
	}
	counts, err := readAtBatchInto(handleID, offsets, dst, slotSize, deadlineMS)
	return resultJSON(counts, err)
}

// CancelFileReadJSON aborts any in-flight reads on the handle. The handle
// remains usable; future reads are unaffected.
func CancelFileReadJSON(handleID string) string {
	handle, err := getFile(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	handle.reads.cancelAll()
	return resultJSON(nil, nil)
}

// CancelFileReadRequestJSON cancels one caller-owned read request.
func CancelFileReadRequestJSON(handleID string, requestID int64) string {
	handle, err := getFile(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	if requestID <= 0 || !handle.reads.cancel(uint64(requestID)) {
		return resultJSON(nil, wrapError(fmt.Errorf("mobile: unknown active read request %d", requestID)))
	}
	return resultJSON(nil, nil)
}

// OpenFileStreamJSON opens a bounded-memory sequential stream. Use
// ReadFileStreamInto with a reusable 256 KiB-1 MiB buffer, then close it with
// CloseFileJSON.
func OpenFileStreamJSON(coreID, path string, optionsRaw string) string {
	options, err := parseOpenFileOptions(optionsRaw)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	priority, err := options.priority()
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	stream, err := openFileStream(s, path, priority, options.DeadlineMS)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	id, err := newID()
	if err != nil {
		_ = stream.Close()
		return resultJSON(nil, wrapError(err))
	}
	registry.mu.Lock()
	registry.files[id] = &fileHandle{
		coreID:       coreID,
		path:         path,
		readPriority: priority,
		readSession:  nextReadSessionID(),
		stream:       stream,
	}
	if reader, ok := stream.(vfs.ContextReader); ok {
		registry.files[id].streamReader = reader
	}
	registry.mu.Unlock()
	return resultJSON(id, nil)
}

// ReadFileStreamInto reads the next sequential bytes from a stream handle.
// The underlying stream is intentionally serialized because io.Reader is not
// safe for concurrent use.
func ReadFileStreamInto(handleID string, dst []byte, deadlineMS int) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	handle, err := getFile(handleID)
	if err != nil {
		return 0, wrapError(err)
	}
	if handle.stream == nil {
		return 0, wrapError(fmt.Errorf("mobile: file handle is not a sequential stream"))
	}
	handle.streamMu.Lock()
	defer handle.streamMu.Unlock()
	if handle.streamClosed {
		return 0, wrapError(fmt.Errorf("mobile: sequential stream is closed"))
	}
	ctx, done, _, _, _, err := handle.reads.begin(deadlineMS)
	if err != nil {
		return 0, wrapError(err)
	}
	defer done()
	ctx = vfs.WithReadPriority(ctx, handle.readPriority)
	if handle.streamReader != nil {
		n, err := handle.streamReader.ReadContext(ctx, dst)
		if err != nil {
			return n, wrapError(err)
		}
		return n, nil
	}
	n, err := handle.stream.Read(dst)
	if err != nil {
		return n, wrapError(err)
	}
	return n, nil
}

func closeFile(handleID string) error {
	registry.mu.Lock()
	handle, ok := registry.files[handleID]
	if !ok {
		registry.mu.Unlock()
		return wrapError(fmt.Errorf("mobile: unknown file handle %q", handleID))
	}
	delete(registry.files, handleID)
	registry.mu.Unlock()
	handle.reads.cancelAll()
	handle.streamMu.Lock()
	handle.streamClosed = true
	stream := handle.stream
	handle.streamMu.Unlock()
	s, err := getSession(handle.coreID)
	if err == nil {
		_ = withCoreErr(s, func(c *core.Core) error {
			c.ReleaseReadSession(handle.readSession)
			return nil
		})
	}
	if stream != nil {
		return wrapError(stream.Close())
	}
	return nil
}

func CloseFileJSON(handleID string) string {
	return resultJSON(nil, closeFile(handleID))
}
