package control

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"
)

// OperationIDHeader carries an idempotency key for a write operation. Two
// requests that carry the same id are executed once: the second one replays
// the first response. Clients that may retry a mutation after a timeout should
// reuse the id (see WithOperationID).
const OperationIDHeader = "X-Qrypt-Operation-Id"

const (
	operationMemoTTL = 10 * time.Minute
	operationMemoMax = 1024
)

type operationIDContextKey struct{}

// WithOperationID annotates ctx with an idempotency key. Transport calls made
// with the derived context send OperationIDHeader, so a retry can safely reuse
// the key after an ambiguous failure.
func WithOperationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, operationIDContextKey{}, id)
}

func operationID(ctx context.Context) string {
	id, _ := ctx.Value(operationIDContextKey{}).(string)
	return id
}

// OperationIDFromContext returns the idempotency key carried by ctx, if any.
func OperationIDFromContext(ctx context.Context) string {
	return operationID(ctx)
}

type operationMemoEntry struct {
	at      time.Time
	status  int
	header  http.Header
	payload []byte
}

// operationMemo remembers successful mutation responses per idempotency key.
type operationMemo struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]operationMemoEntry
}

func newOperationMemo() *operationMemo {
	return &operationMemo{now: time.Now, entries: make(map[string]operationMemoEntry)}
}

func (m *operationMemo) lookup(id string) (operationMemoEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[id]
	if !ok {
		return operationMemoEntry{}, false
	}
	if m.now().Sub(entry.at) > operationMemoTTL {
		delete(m.entries, id)
		return operationMemoEntry{}, false
	}
	return entry, true
}

func (m *operationMemo) store(id string, status int, header http.Header, payload []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) >= operationMemoMax {
		// Bounded growth: drop the oldest entry. Operations are short-lived,
		// and a dropped key only loses replay protection, never correctness.
		var oldestID string
		var oldest time.Time
		for key, entry := range m.entries {
			if oldestID == "" || entry.at.Before(oldest) {
				oldestID, oldest = key, entry.at
			}
		}
		delete(m.entries, oldestID)
	}
	cloned := make(http.Header, len(header))
	for key, values := range header {
		cloned[key] = append([]string(nil), values...)
	}
	m.entries[id] = operationMemoEntry{
		at:      m.now(),
		status:  status,
		header:  cloned,
		payload: append([]byte(nil), payload...),
	}
}

// bufferedResponseWriter captures a mutation handler's response so it can be
// replayed for a repeated operation id.
type bufferedResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header)}
}

func (w *bufferedResponseWriter) Header() http.Header { return w.header }

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func (w *bufferedResponseWriter) flushTo(dst http.ResponseWriter) {
	for key, values := range w.header {
		for _, value := range values {
			dst.Header().Add(key, value)
		}
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	dst.WriteHeader(status)
	_, _ = dst.Write(w.body.Bytes())
}
