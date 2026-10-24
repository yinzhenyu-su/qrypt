// Package credentialtest provides an in-memory drive.CredentialSession for
// driver tests that need cross-mount propagation without the core manager.
package credentialtest

import (
	"context"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type Session struct {
	name string

	mu      sync.Mutex
	payload []byte
	version uint64
	account string
	subs    []func(drive.CredentialSnapshot)
}

func New(name string) *Session {
	return &Session{name: name}
}

func (s *Session) Name() string { return s.name }

func (s *Session) Snapshot() drive.CredentialSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Session) snapshotLocked() drive.CredentialSnapshot {
	return drive.CredentialSnapshot{
		Payload: append([]byte(nil), s.payload...),
		Version: s.version,
		Account: s.account,
	}
}

func (s *Session) Update(expected uint64, payload []byte, account string) (drive.CredentialSnapshot, error) {
	s.mu.Lock()
	if expected != s.version {
		snapshot := s.snapshotLocked()
		s.mu.Unlock()
		return snapshot, drive.ErrCredentialVersionConflict
	}
	s.payload = append([]byte(nil), payload...)
	s.account = account
	s.version++
	snapshot := s.snapshotLocked()
	subs := append([]func(drive.CredentialSnapshot){}, s.subs...)
	s.mu.Unlock()
	for _, fn := range subs {
		fn(snapshot)
	}
	return snapshot, nil
}

func (s *Session) Subscribe(fn func(drive.CredentialSnapshot)) func() {
	s.mu.Lock()
	index := len(s.subs)
	s.subs = append(s.subs, fn)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if index < len(s.subs) {
			s.subs = append(s.subs[:index], s.subs[index+1:]...)
		}
		s.mu.Unlock()
	}
}

func (s *Session) Refresh(ctx context.Context, fn func(drive.CredentialSnapshot) ([]byte, string, error)) (drive.CredentialSnapshot, error) {
	current := s.Snapshot()
	payload, account, err := fn(current)
	if err != nil {
		return current, err
	}
	return s.Update(current.Version, payload, account)
}
