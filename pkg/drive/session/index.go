package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	// DefaultMaxEntries bounds the per-driver binding file size. Bindings are
	// tiny (a token, not part state), so this is a generous ceiling.
	DefaultMaxEntries = 4096
	// DefaultTouchWriteInterval throttles Touch() disk writes. Uploads that
	// last longer than the expiry age would otherwise be reclaimed despite
	// being alive; one write per interval keeps the on-disk timestamp fresh
	// without per-part I/O.
	DefaultTouchWriteInterval = time.Minute
)

// Session is the persisted binding of an upload: content key → provider
// upload reference. Token is driver-private JSON.
type Session struct {
	Key       string          `json:"key"`
	Token     json.RawMessage `json:"token,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// IndexOptions configures an Index.
type IndexOptions struct {
	MaxEntries         int           // 0 = DefaultMaxEntries
	TouchWriteInterval time.Duration // 0 = DefaultTouchWriteInterval
	OnError            func(error)
}

// Index is one driver's binding store: an in-memory map backed by a single
// small JSON file written atomically. Mutations are rare (once per upload),
// so every write is synchronous and the whole file is rewritten; upload
// progress is deliberately absent from the file.
type Index struct {
	mu                 sync.Mutex
	store              drive.StateStore
	file               string
	sessions           map[string]Session
	loaded             bool
	maxEntries         int
	touchWriteInterval time.Duration
	onError            func(error)
	lastWrite          time.Time
}

// NewIndex creates a binding index persisted through store under file.
func NewIndex(store drive.StateStore, file string, opts IndexOptions) *Index {
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = DefaultMaxEntries
	}
	if opts.TouchWriteInterval <= 0 {
		opts.TouchWriteInterval = DefaultTouchWriteInterval
	}
	return &Index{
		store:              store,
		file:               file,
		sessions:           map[string]Session{},
		maxEntries:         opts.MaxEntries,
		touchWriteInterval: opts.TouchWriteInterval,
		onError:            opts.OnError,
	}
}

// Get returns the binding for key.
func (x *Index) Get(key string) (Session, bool) {
	if x == nil || key == "" {
		return Session{}, false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.ensureLoaded()
	s, ok := x.sessions[key]
	return s, ok
}

// List returns a snapshot of all bindings, for expiry scans and diagnostics.
func (x *Index) List() []Session {
	if x == nil {
		return nil
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.ensureLoaded()
	out := make([]Session, 0, len(x.sessions))
	for _, s := range x.sessions {
		s.Token = append(json.RawMessage(nil), s.Token...)
		out = append(out, s)
	}
	return out
}

// Create reserves or updates a binding synchronously. Callers must persist
// the binding before starting provider upload work, so a crash at any point
// leaves either a resumable or a reclaimable state. Re-persisting an existing
// key overwrites its token (fresh attempt) and resets its timestamps.
func (x *Index) Create(key string, token json.RawMessage) error {
	if x == nil || key == "" {
		return nil
	}
	now := time.Now()
	return x.mutate(func(next map[string]Session) {
		next[key] = Session{Key: key, Token: token, CreatedAt: now, UpdatedAt: now}
		if len(next) > x.maxEntries {
			evictOldest(next)
		}
	})
}

// Delete removes a binding best-effort. Upload success never depends on the
// cleanup: a leftover binding is reclaimed by the expiry pass, and a binding
// pointing at an already-completed upload is detected as invalid on the next
// attempt.
func (x *Index) Delete(key string) {
	if x == nil || key == "" {
		return
	}
	_ = x.mutate(func(next map[string]Session) {
		delete(next, key)
	})
}

// Flush persists the current in-memory bindings immediately, collapsing any
// throttled Touch updates. Drivers call it on graceful shutdown (Drop) so the
// on-disk index matches memory before the process exits; the returned error is
// also reported through OnError.
func (x *Index) Flush() error {
	if x == nil {
		return nil
	}
	return x.write()
}

// Touch refreshes the in-memory activity timestamp and persists it at a
// throttled rate, so long uploads stay alive in the expiry scan without
// per-part disk writes.
func (x *Index) Touch(key string) {
	x.TouchWith(key, func(s *Session) { s.UpdatedAt = time.Now() })
}

// TouchWith lets a driver fold compact progress (e.g. a confirmed-parts
// bitmap carried inside Token) into the in-memory binding, persisted at the
// same throttled rate as Touch. The update closure runs under the index lock,
// so drivers that confirm parts concurrently must perform their token
// read-modify-write inside it — that serializes bit/etag updates without lost
// marks. Progress written this way may lag the latest confirmations by up to
// one interval, so a driver must only skip work that is idempotently
// re-doable (provider part uploads overwrite by number/seq); the
// content-addressed Key still guarantees a change of content resets progress.
func (x *Index) TouchWith(key string, update func(*Session)) {
	if x == nil || x.store == nil || key == "" || update == nil {
		return
	}
	now := time.Now()
	x.mu.Lock()
	x.ensureLoaded()
	s, ok := x.sessions[key]
	if !ok {
		x.mu.Unlock()
		return
	}
	update(&s)
	x.sessions[key] = s
	persist := x.lastWrite.IsZero() || now.Sub(x.lastWrite) >= x.touchWriteInterval
	x.mu.Unlock()
	if persist {
		_ = x.write()
	}
}

// Expire reclaims bindings idle for longer than maxAge. For each candidate it
// first runs reclaim (idempotent provider-side cleanup, provided by the
// driver); the binding is removed only after reclaim succeeds, so transient
// failures keep the binding for the next pass. A binding that was touched or
// recreated while reclaim ran is left alone.
func (x *Index) Expire(maxAge time.Duration, now time.Time, reclaim func(Session) error) {
	if x == nil || maxAge <= 0 {
		return
	}
	var expired []Session
	x.mu.Lock()
	x.ensureLoaded()
	for _, s := range x.sessions {
		if !s.UpdatedAt.IsZero() && now.Sub(s.UpdatedAt) > maxAge {
			expired = append(expired, s)
		}
	}
	x.mu.Unlock()
	if len(expired) == 0 {
		return
	}
	reclaimed := expired[:0]
	for _, s := range expired {
		if reclaim != nil {
			if err := reclaim(s); err != nil {
				x.report(fmt.Errorf("expire reclaim: %w", err))
				continue
			}
		}
		reclaimed = append(reclaimed, s)
	}
	if len(reclaimed) == 0 {
		return
	}
	_ = x.mutate(func(next map[string]Session) {
		for _, s := range reclaimed {
			cur, ok := next[s.Key]
			if ok && cur.UpdatedAt.Equal(s.UpdatedAt) && bytes.Equal(cur.Token, s.Token) {
				delete(next, s.Key)
			}
		}
	})
}

// mutate applies f to a copy of the current bindings, persists the result, and
// only then commits the copy to memory. A failed write leaves both memory and
// disk at the previous state, so callers can abort provider work without a
// phantom binding lingering in the running process.
func (x *Index) mutate(f func(next map[string]Session)) error {
	x.mu.Lock()
	x.ensureLoaded()
	next := cloneSessions(x.sessions)
	f(next)
	x.mu.Unlock()
	if x.store != nil {
		if err := x.store.SaveJSON(x.file, next); err != nil {
			x.report(err)
			return err
		}
	}
	x.mu.Lock()
	x.sessions = next
	x.lastWrite = time.Now()
	x.mu.Unlock()
	return nil
}

// RunExpirer periodically runs expire until ctx is done. Drivers start it
// after the state store is installed and cancel it on Drop. A non-positive
// interval disables the loop (time.NewTicker would panic).
func RunExpirer(ctx context.Context, interval time.Duration, expire func()) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if expire != nil {
				expire()
			}
		}
	}
}

func (x *Index) ensureLoaded() {
	if x.loaded {
		return
	}
	x.loaded = true
	if x.store == nil {
		return
	}
	var state map[string]Session
	if err := x.store.LoadJSON(x.file, &state); err != nil {
		return
	}
	if state != nil {
		x.sessions = state
	}
}

func (x *Index) write() error {
	x.mu.Lock()
	if x.store == nil {
		x.mu.Unlock()
		return nil
	}
	snapshot := cloneSessions(x.sessions)
	x.mu.Unlock()
	if err := x.store.SaveJSON(x.file, snapshot); err != nil {
		x.report(err)
		return err
	}
	x.mu.Lock()
	x.lastWrite = time.Now()
	x.mu.Unlock()
	return nil
}

// cloneSessions deep-copies the bindings so callers can snapshot and mutate
// without aliasing live entries or the last token slice handed to SaveJSON.
func cloneSessions(src map[string]Session) map[string]Session {
	next := make(map[string]Session, len(src))
	for k, s := range src {
		s.Token = append(json.RawMessage(nil), s.Token...)
		next[k] = s
	}
	return next
}

// evictOldest removes the least recently updated binding. Callers invoke it
// only when the map has grown past MaxEntries.
func evictOldest(m map[string]Session) {
	var oldestKey string
	var oldest time.Time
	for k, s := range m {
		if oldestKey == "" || s.UpdatedAt.Before(oldest) {
			oldestKey, oldest = k, s.UpdatedAt
		}
	}
	delete(m, oldestKey)
}

func (x *Index) report(err error) {
	if err == nil || x.onError == nil {
		return
	}
	x.onError(fmt.Errorf("%s: %w", x.file, err))
}
