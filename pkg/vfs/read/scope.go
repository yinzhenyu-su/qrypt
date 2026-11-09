package read

import (
	"context"
	"sync"
)

type readScopeKey struct {
	cacheKey  string
	sessionID uint64
}

// ReadScope owns the lifetime of speculative prefetch for one file/session.
// Foreground reads never use it; they keep their caller context.
type ReadScope struct {
	ctx    context.Context
	cancel context.CancelFunc
	refs   int
	mu     sync.Mutex
	reason string
}

func (s *ReadScope) setReason(reason string) {
	s.mu.Lock()
	s.reason = reason
	s.mu.Unlock()
}

func (s *ReadScope) cancelReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

// ScopeRegistry is the single owner of speculative read lifetimes. Its key is
// (cacheKey, sessionID), so a file or an open-file session can be canceled
// without touching unrelated reads.
type ScopeRegistry struct {
	mu        sync.Mutex
	scopes    map[readScopeKey]*ReadScope
	byCache   map[string]map[readScopeKey]struct{}
	bySession map[uint64]map[readScopeKey]struct{}
}

func newScopeRegistry() *ScopeRegistry {
	return &ScopeRegistry{
		scopes:    map[readScopeKey]*ReadScope{},
		byCache:   map[string]map[readScopeKey]struct{}{},
		bySession: map[uint64]map[readScopeKey]struct{}{},
	}
}

// Acquire returns the scope for the context's session and cache key, creating
// it on first use. The release function drops one reference; the scope is
// canceled when the last reference goes away.
func (r *ScopeRegistry) Acquire(ctx context.Context, cacheKey string) (*ReadScope, func()) {
	hint, _ := AccessHintFromContext(ctx)
	key := readScopeKey{cacheKey: cacheKey, sessionID: hint.SessionID}
	r.mu.Lock()
	scope := r.scopes[key]
	if scope == nil {
		scopeCtx, cancel := context.WithCancel(context.Background())
		scope = &ReadScope{ctx: scopeCtx, cancel: cancel}
		r.scopes[key] = scope
		if cacheKey != "" {
			addScopeIndex(r.byCache, cacheKey, key)
		}
		if hint.SessionID != 0 {
			addScopeIndex(r.bySession, hint.SessionID, key)
		}
	}
	scope.refs++
	r.mu.Unlock()

	release := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		scope.refs--
		if scope.refs > 0 || r.scopes[key] != scope {
			return
		}
		delete(r.scopes, key)
		removeScopeIndex(r.byCache, cacheKey, key)
		if hint.SessionID != 0 {
			removeScopeIndex(r.bySession, hint.SessionID, key)
		}
		scope.setReason("idle")
		scope.cancel()
	}
	return scope, release
}

// CancelFile cancels every scope for cacheKey. Canceled scopes are removed
// from the registry so a later prefetch for the same file starts with a fresh
// lifetime instead of inheriting the canceled context.
func (r *ScopeRegistry) CancelFile(cacheKey, reason string) {
	if r == nil || cacheKey == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.byCache[cacheKey] {
		if scope := r.scopes[key]; scope != nil {
			scope.setReason(reason)
			scope.cancel()
			r.removeLocked(key)
		}
	}
}

// CancelSession cancels every scope registered for sessionID.
func (r *ScopeRegistry) CancelSession(sessionID uint64, reason string) {
	if r == nil || sessionID == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.bySession[sessionID] {
		if scope := r.scopes[key]; scope != nil {
			scope.setReason(reason)
			scope.cancel()
			r.removeLocked(key)
		}
	}
}

// CancelAll cancels every scope.
func (r *ScopeRegistry) CancelAll(reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, scope := range r.scopes {
		scope.setReason(reason)
		scope.cancel()
		r.removeLocked(key)
	}
}

// Cancel cancels the scope matching the context's session and cacheKey.
func (r *ScopeRegistry) Cancel(ctx context.Context, cacheKey, reason string) {
	if r == nil || cacheKey == "" {
		return
	}
	hint, _ := AccessHintFromContext(ctx)
	key := readScopeKey{cacheKey: cacheKey, sessionID: hint.SessionID}
	r.mu.Lock()
	defer r.mu.Unlock()
	if scope := r.scopes[key]; scope != nil {
		scope.setReason(reason)
		scope.cancel()
		r.removeLocked(key)
	}
}

func (r *ScopeRegistry) removeLocked(key readScopeKey) {
	delete(r.scopes, key)
	removeScopeIndex(r.byCache, key.cacheKey, key)
	if key.sessionID != 0 {
		removeScopeIndex(r.bySession, key.sessionID, key)
	}
}

func addScopeIndex[K comparable](index map[K]map[readScopeKey]struct{}, key K, scopeKey readScopeKey) {
	keys := index[key]
	if keys == nil {
		keys = map[readScopeKey]struct{}{}
		index[key] = keys
	}
	keys[scopeKey] = struct{}{}
}

func removeScopeIndex[K comparable](index map[K]map[readScopeKey]struct{}, key K, scopeKey readScopeKey) {
	keys := index[key]
	if keys == nil {
		return
	}
	delete(keys, scopeKey)
	if len(keys) == 0 {
		delete(index, key)
	}
}

// --- State wrappers (existing call sites and tests) ---

func (s *State) scopeFor(ctx context.Context, cacheKey string) (*ReadScope, func()) {
	return s.scopes.Acquire(ctx, cacheKey)
}

func (s *State) cancelScopesByCacheKey(cacheKey, reason string) {
	if s == nil || s.scopes == nil || cacheKey == "" {
		return
	}
	s.scopes.CancelFile(cacheKey, reason)
}

func (s *State) cancelScopesBySession(sessionID uint64, reason string) {
	if s == nil || s.scopes == nil || sessionID == 0 {
		return
	}
	s.scopes.CancelSession(sessionID, reason)
}

func (s *State) cancelAllScopes(reason string) {
	if s == nil || s.scopes == nil {
		return
	}
	s.scopes.CancelAll(reason)
}

func (s *State) cancelReadScope(ctx context.Context, cacheKey, reason string) {
	if s == nil || s.scopes == nil || cacheKey == "" {
		return
	}
	s.scopes.Cancel(ctx, cacheKey, reason)
}

// CancelPrefetchForFile stops speculative prefetch for one file without
// dropping completed cache fills. Mobile uses it when a handle closes.
func (s *State) CancelPrefetchForFile(cacheKey string) {
	s.cancelScopesByCacheKey(cacheKey, "caller")
}
