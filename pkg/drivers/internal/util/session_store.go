package util

import (
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type UploadSessionStore[T any] struct {
	store      drive.StateStore
	file       string
	maxAge     time.Duration
	maxEntries int
	key        func(T) string
	valid      func(string, T) bool
	updatedAt  func(T) time.Time
	touch      func(*T, time.Time)
	onError    func(error)
}

type uploadSessionState[T any] struct {
	Version  int          `json:"version"`
	Sessions map[string]T `json:"sessions,omitempty"`
}

type UploadSessionStoreOptions[T any] struct {
	Store      drive.StateStore
	File       string
	MaxAge     time.Duration
	MaxEntries int
	Key        func(T) string
	Valid      func(string, T) bool
	UpdatedAt  func(T) time.Time
	Touch      func(*T, time.Time)
	OnError    func(error)
}

func NewUploadSessionStore[T any](opts UploadSessionStoreOptions[T]) *UploadSessionStore[T] {
	return &UploadSessionStore[T]{
		store:      opts.Store,
		file:       opts.File,
		maxAge:     opts.MaxAge,
		maxEntries: opts.MaxEntries,
		key:        opts.Key,
		valid:      opts.Valid,
		updatedAt:  opts.UpdatedAt,
		touch:      opts.Touch,
		onError:    opts.OnError,
	}
}

func (s *UploadSessionStore[T]) Load(key string) (T, bool) {
	var zero T
	if s == nil || s.store == nil || key == "" {
		return zero, false
	}
	state, changed := s.prunedState(s.loadState(), time.Now())
	if changed {
		_ = s.saveState(state)
	}
	session, ok := state.Sessions[key]
	if !ok || !s.isValid(key, session) {
		return zero, false
	}
	return session, true
}

func (s *UploadSessionStore[T]) Save(session T) {
	if s == nil || s.store == nil {
		return
	}
	key := s.sessionKey(session)
	if key == "" {
		return
	}
	if s.touch != nil {
		s.touch(&session, time.Now())
	}
	state := s.loadState()
	state.Version = 1
	state.Sessions[key] = session
	state, _ = s.prunedState(state, time.Now())
	if err := s.saveState(state); err != nil {
		s.report(err)
	}
}

func (s *UploadSessionStore[T]) Delete(key string) {
	if s == nil || s.store == nil || key == "" {
		return
	}
	state, _ := s.prunedState(s.loadState(), time.Now())
	if _, ok := state.Sessions[key]; !ok {
		return
	}
	delete(state.Sessions, key)
	state.Version = 1
	if err := s.saveState(state); err != nil {
		s.report(err)
	}
}

func (s *UploadSessionStore[T]) Prune() {
	if s == nil || s.store == nil {
		return
	}
	state, changed := s.prunedState(s.loadState(), time.Now())
	if !changed {
		return
	}
	if err := s.saveState(state); err != nil {
		s.report(err)
	}
}

func (s *UploadSessionStore[T]) PrunedForTest(state map[string]T, now time.Time) (map[string]T, bool) {
	wrapped := uploadSessionState[T]{Version: 1, Sessions: state}
	pruned, changed := s.prunedState(wrapped, now)
	return pruned.Sessions, changed
}

func (s *UploadSessionStore[T]) loadState() uploadSessionState[T] {
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

func (s *UploadSessionStore[T]) saveState(state uploadSessionState[T]) error {
	if s == nil || s.store == nil {
		return nil
	}
	state.Version = 1
	if state.Sessions == nil {
		state.Sessions = map[string]T{}
	}
	return s.store.SaveJSON(s.file, state)
}

func (s *UploadSessionStore[T]) prunedState(state uploadSessionState[T], now time.Time) (uploadSessionState[T], bool) {
	state.Version = 1
	if state.Sessions == nil {
		state.Sessions = map[string]T{}
		return state, false
	}
	changed := PruneSessions(state.Sessions, now, s.maxAge, s.maxEntries, s.isValid, s.sessionUpdatedAt)
	return state, changed
}

func (s *UploadSessionStore[T]) sessionKey(session T) string {
	if s.key == nil {
		return ""
	}
	return s.key(session)
}

func (s *UploadSessionStore[T]) isValid(key string, session T) bool {
	if s.valid == nil {
		return key != ""
	}
	return s.valid(key, session)
}

func (s *UploadSessionStore[T]) sessionUpdatedAt(session T) time.Time {
	if s.updatedAt == nil {
		return time.Time{}
	}
	return s.updatedAt(session)
}

func (s *UploadSessionStore[T]) report(err error) {
	if err == nil {
		return
	}
	if s.onError != nil {
		s.onError(fmt.Errorf("%s: %w", s.file, err))
	}
}
