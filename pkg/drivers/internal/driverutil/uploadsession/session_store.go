package uploadsession

import (
	"fmt"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type Store[T any] struct {
	store      drive.StateStore
	file       string
	maxAge     time.Duration
	maxEntries int
	key        func(T) string
	valid      func(string, T) bool
	updatedAt  func(T) time.Time
	touch      func(*T, time.Time)
	clone      func(T) T
	onError    func(error)
	async      bool

	mu             sync.Mutex
	state          uploadSessionState[T]
	loaded         bool
	pending        *pendingWrite[T]
	nextVersion    uint64
	writtenVersion uint64
	workerStarted  bool
	closed         bool
	cond           *sync.Cond
	workerDone     chan struct{}
}

type uploadSessionState[T any] struct {
	Version  int          `json:"version"`
	Sessions map[string]T `json:"sessions,omitempty"`
}

type StoreOptions[T any] struct {
	Store      drive.StateStore
	File       string
	MaxAge     time.Duration
	MaxEntries int
	Key        func(T) string
	Valid      func(string, T) bool
	UpdatedAt  func(T) time.Time
	Touch      func(*T, time.Time)
	Clone      func(T) T
	OnError    func(error)
	Async      bool
}

type pendingWrite[T any] struct {
	version uint64
	state   uploadSessionState[T]
}

func NewStore[T any](opts StoreOptions[T]) *Store[T] {
	store := &Store[T]{
		store:      opts.Store,
		file:       opts.File,
		maxAge:     opts.MaxAge,
		maxEntries: opts.MaxEntries,
		key:        opts.Key,
		valid:      opts.Valid,
		updatedAt:  opts.UpdatedAt,
		touch:      opts.Touch,
		clone:      opts.Clone,
		onError:    opts.OnError,
		async:      opts.Async,
	}
	store.cond = sync.NewCond(&store.mu)
	store.workerDone = make(chan struct{})
	return store
}

func (s *Store[T]) Load(key string) (T, bool) {
	var zero T
	if s == nil || s.store == nil || key == "" {
		return zero, false
	}
	s.mu.Lock()
	s.ensureLoadedLocked()
	session, ok := s.state.Sessions[key]
	s.mu.Unlock()
	if !ok || !s.isValid(key, session) {
		return zero, false
	}
	return session, true
}

// LoadAs loads a session and maps it to the representation needed by a
// caller. The generic method keeps the store's persistence type separate from
// driver-specific load normalization.
func (s *Store[T]) LoadAs[U any](key string, mapFn func(T) U) (U, bool) {
	session, ok := s.Load(key)
	if !ok {
		var zero U
		return zero, false
	}
	return mapFn(session), true
}

func (s *Store[T]) Save(session T) {
	if s == nil || s.store == nil {
		return
	}
	key := s.sessionKey(session)
	if key == "" {
		return
	}
	s.mu.Lock()
	s.ensureLoadedLocked()
	if s.touch != nil {
		s.touch(&session, time.Now())
	}
	s.state.Sessions[key] = session
	s.state, _ = s.prunedState(s.state, time.Now())
	s.enqueueLocked()
	s.mu.Unlock()
}

func (s *Store[T]) Delete(key string) {
	if s == nil || s.store == nil || key == "" {
		return
	}
	s.mu.Lock()
	s.ensureLoadedLocked()
	if _, ok := s.state.Sessions[key]; !ok {
		s.mu.Unlock()
		return
	}
	delete(s.state.Sessions, key)
	version := s.enqueueLocked()
	s.mu.Unlock()
	s.waitForVersion(version)
}

func (s *Store[T]) Prune() {
	if s == nil || s.store == nil {
		return
	}
	s.mu.Lock()
	s.ensureLoadedLocked()
	var changed bool
	s.state, changed = s.prunedState(s.state, time.Now())
	if !changed {
		s.mu.Unlock()
		return
	}
	version := s.enqueueLocked()
	s.mu.Unlock()
	s.waitForVersion(version)
}

func (s *Store[T]) Flush() {
	if s == nil || s.store == nil {
		return
	}
	s.mu.Lock()
	s.ensureLoadedLocked()
	version := s.nextVersion
	s.mu.Unlock()
	s.waitForVersion(version)
}

func (s *Store[T]) Close() {
	if s == nil || s.store == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if !s.workerStarted {
		s.mu.Unlock()
		return
	}
	s.cond.Broadcast()
	done := s.workerDone
	s.mu.Unlock()
	<-done
}

func (s *Store[T]) PrunedForTest(state map[string]T, now time.Time) (map[string]T, bool) {
	wrapped := uploadSessionState[T]{Version: 1, Sessions: state}
	pruned, changed := s.prunedState(wrapped, now)
	return pruned.Sessions, changed
}

func (s *Store[T]) loadState() uploadSessionState[T] {
	state := uploadSessionState[T]{Version: 1, Sessions: map[string]T{}}
	if s == nil || s.store == nil {
		return state
	}
	if err := s.store.LoadJSON(s.file, &state); err != nil {
		return uploadSessionState[T]{Version: 1, Sessions: map[string]T{}}
	}
	if state.Sessions == nil {
		state.Sessions = map[string]T{}
	}
	return state
}

func (s *Store[T]) ensureLoadedLocked() {
	if s.loaded {
		return
	}
	s.state = s.loadState()
	var changed bool
	s.state, changed = s.prunedState(s.state, time.Now())
	s.loaded = true
	if changed {
		s.enqueueLocked()
	}
}

func (s *Store[T]) enqueueLocked() uint64 {
	s.nextVersion++
	version := s.nextVersion
	if !s.async {
		if err := s.saveState(s.stateSnapshotLocked()); err != nil {
			s.report(err)
		}
		s.writtenVersion = version
		return version
	}
	if s.closed {
		if err := s.saveState(s.stateSnapshotLocked()); err != nil {
			s.report(err)
		}
		s.writtenVersion = version
		return version
	}
	if !s.workerStarted {
		s.workerStarted = true
		go s.writeLoop()
	}
	s.pending = &pendingWrite[T]{version: version, state: s.stateSnapshotLocked()}
	s.cond.Signal()
	return version
}

func (s *Store[T]) stateSnapshotLocked() uploadSessionState[T] {
	snapshot := uploadSessionState[T]{Version: s.state.Version, Sessions: make(map[string]T, len(s.state.Sessions))}
	for key, session := range s.state.Sessions {
		if s.clone != nil {
			session = s.clone(session)
		}
		snapshot.Sessions[key] = session
	}
	return snapshot
}

func (s *Store[T]) waitForVersion(version uint64) {
	if version == 0 || s == nil || s.store == nil {
		return
	}
	s.mu.Lock()
	for s.writtenVersion < version && !s.closed {
		s.cond.Wait()
	}
	s.mu.Unlock()
}

func (s *Store[T]) writeLoop() {
	defer close(s.workerDone)
	for {
		s.mu.Lock()
		for s.pending == nil && !s.closed {
			s.cond.Wait()
		}
		if s.pending == nil && s.closed {
			s.mu.Unlock()
			return
		}
		pending := s.pending
		s.pending = nil
		s.mu.Unlock()

		err := s.saveState(pending.state)

		s.mu.Lock()
		if pending.version > s.writtenVersion {
			s.writtenVersion = pending.version
		}
		s.cond.Broadcast()
		s.mu.Unlock()
		s.report(err)
	}
}

func (s *Store[T]) saveState(state uploadSessionState[T]) error {
	if s == nil || s.store == nil {
		return nil
	}
	state.Version = 1
	if state.Sessions == nil {
		state.Sessions = map[string]T{}
	}
	return s.store.SaveJSON(s.file, state)
}

func (s *Store[T]) prunedState(state uploadSessionState[T], now time.Time) (uploadSessionState[T], bool) {
	state.Version = 1
	if state.Sessions == nil {
		state.Sessions = map[string]T{}
		return state, false
	}
	changed := PruneSessions(state.Sessions, now, s.maxAge, s.maxEntries, s.isValid, s.sessionUpdatedAt)
	return state, changed
}

func (s *Store[T]) sessionKey(session T) string {
	if s.key == nil {
		return ""
	}
	return s.key(session)
}

func (s *Store[T]) isValid(key string, session T) bool {
	if s.valid == nil {
		return key != ""
	}
	return s.valid(key, session)
}

func (s *Store[T]) sessionUpdatedAt(session T) time.Time {
	if s.updatedAt == nil {
		return time.Time{}
	}
	return s.updatedAt(session)
}

func (s *Store[T]) report(err error) {
	if err == nil {
		return
	}
	if s.onError != nil {
		s.onError(fmt.Errorf("%s: %w", s.file, err))
	}
}
