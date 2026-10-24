package credentials

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"golang.org/x/sync/singleflight"
)

// Session implements drive.CredentialSession. The payload is opaque to this
// package; the owning driver encodes and decodes it.
type Session struct {
	name  string
	store fileStore
	seed  string

	mu        sync.RWMutex
	payload   []byte
	version   uint64
	account   string
	updatedAt time.Time

	subs    map[uint64]func(drive.CredentialSnapshot)
	nextSub uint64

	refresh singleflight.Group
}

func newSession(store fileStore, spec Spec) *Session {
	session := &Session{
		name:  spec.Name,
		store: store,
		seed:  spec.ParamsSeed,
		subs:  map[uint64]func(drive.CredentialSnapshot){},
	}
	state, err := store.load(spec.Name)
	if err != nil {
		session.updatedAt = time.Now().UTC()
		return session
	}
	if state.ParamsSeed == spec.ParamsSeed {
		session.payload = append([]byte(nil), state.Data...)
		session.version = state.Version
		session.account = state.Account
		session.updatedAt = state.UpdatedAt
		return session
	}
	// The credential params were replaced: the session belonged to the old
	// params, so it must not survive. Record the new seed durably.
	session.version = state.Version + 1
	session.updatedAt = time.Now().UTC()
	if err := store.overwrite(spec.Name, session.fileLocked()); err != nil {
		// Persistence is best-effort at open time; the in-memory reset still
		// prevents the stale session from being used by this process.
		session.version--
	}
	return session
}

func (s *Session) Name() string { return s.name }

func (s *Session) Snapshot() drive.CredentialSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *Session) snapshotLocked() drive.CredentialSnapshot {
	return drive.CredentialSnapshot{
		Payload:   append([]byte(nil), s.payload...),
		Version:   s.version,
		Account:   s.account,
		UpdatedAt: s.updatedAt,
	}
}

func (s *Session) Update(expected uint64, payload []byte, account string) (drive.CredentialSnapshot, error) {
	s.mu.Lock()
	if expected != s.version {
		snapshot := s.snapshotLocked()
		s.mu.Unlock()
		return snapshot, drive.ErrCredentialVersionConflict
	}
	next := sessionFile{
		Version:    s.version + 1,
		ParamsSeed: s.seed,
		Account:    account,
		UpdatedAt:  time.Now().UTC(),
		Data:       payload,
	}
	if err := s.store.save(s.name, next, s.version); err != nil {
		if errors.Is(err, drive.ErrCredentialVersionConflict) {
			// Another process committed first: adopt its session so later
			// writes stop conflicting and callers observe the newer value.
			if state, loadErr := s.store.load(s.name); loadErr == nil && state.ParamsSeed == s.seed {
				s.payload = append([]byte(nil), state.Data...)
				s.version = state.Version
				s.account = state.Account
				s.updatedAt = state.UpdatedAt
			}
		}
		snapshot := s.snapshotLocked()
		s.mu.Unlock()
		return snapshot, err
	}
	s.version = next.Version
	s.payload = append([]byte(nil), payload...)
	s.account = account
	s.updatedAt = next.UpdatedAt
	snapshot := s.snapshotLocked()
	subs := make([]func(drive.CredentialSnapshot), 0, len(s.subs))
	for _, fn := range s.subs {
		subs = append(subs, fn)
	}
	s.mu.Unlock()
	for _, fn := range subs {
		fn(snapshot)
	}
	return snapshot, nil
}

func (s *Session) Subscribe(fn func(drive.CredentialSnapshot)) (cancel func()) {
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = fn
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subs, id)
			s.mu.Unlock()
		})
	}
}

func (s *Session) Refresh(ctx context.Context, fn func(drive.CredentialSnapshot) ([]byte, string, error)) (drive.CredentialSnapshot, error) {
	result, err, _ := s.refresh.Do("refresh", func() (any, error) {
		lastErr := error(drive.ErrCredentialVersionConflict)
		for attempt := 0; attempt < 2; attempt++ {
			if err := ctx.Err(); err != nil {
				return drive.CredentialSnapshot{}, err
			}
			current := s.Snapshot()
			payload, account, err := fn(current)
			if err != nil {
				return drive.CredentialSnapshot{}, err
			}
			snapshot, err := s.Update(current.Version, payload, account)
			if err == nil {
				return snapshot, nil
			}
			lastErr = err
			if !errors.Is(err, drive.ErrCredentialVersionConflict) {
				return drive.CredentialSnapshot{}, err
			}
		}
		return drive.CredentialSnapshot{}, lastErr
	})
	if err != nil {
		return drive.CredentialSnapshot{}, err
	}
	return result.(drive.CredentialSnapshot), nil
}

// reconcileSeed invalidates a session whose credential params were replaced
// after the session object was created (for example a config update applied to
// a long-lived manager).
func (s *Session) reconcileSeed(seed string) {
	s.mu.Lock()
	if s.seed == seed {
		s.mu.Unlock()
		return
	}
	s.seed = seed
	s.payload = nil
	s.account = ""
	s.version++
	s.updatedAt = time.Now().UTC()
	file := s.fileLocked()
	s.mu.Unlock()
	_ = s.store.overwrite(s.name, file)
}

func (s *Session) fileLocked() sessionFile {
	return sessionFile{
		Version:    s.version,
		ParamsSeed: s.seed,
		Account:    s.account,
		UpdatedAt:  s.updatedAt,
		Data:       append([]byte(nil), s.payload...),
	}
}
