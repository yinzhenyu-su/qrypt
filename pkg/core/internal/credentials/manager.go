// Package credentials owns the process-wide lifecycle of shared credentials:
// one session object per configured credential, versioned commits, persistence,
// cross-mount notifications, and coalesced refresh.
package credentials

import (
	"path/filepath"
	"sync"
)

// Spec identifies one configured credential and the static params it was
// derived from. ParamsSeed changes when the user edits the credential, which
// invalidates the persisted session for every mount that references it.
type Spec struct {
	Name       string
	ParamsSeed string
}

// Manager hands out the single session object for each credential name. It is
// process-wide (keyed by the absolute state directory) because a Core reload
// builds the replacement before closing the old one, and mobile can hold
// several Cores at once.
type Manager struct {
	dir      string
	mu       sync.Mutex
	sessions map[string]*Session
}

var (
	registryMu sync.Mutex
	registry   = map[string]*Manager{}
)

// Open returns the manager for stateDir. Managers are shared by absolute state
// directory; an empty stateDir yields an isolated in-memory manager so tests
// and callers without persistence never share sessions accidentally.
func Open(stateDir string) *Manager {
	if stateDir == "" {
		return &Manager{sessions: map[string]*Session{}}
	}
	key, err := filepath.Abs(stateDir)
	if err != nil {
		key = stateDir
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if manager := registry[key]; manager != nil {
		return manager
	}
	manager := &Manager{dir: key, sessions: map[string]*Session{}}
	registry[key] = manager
	return manager
}

// Session returns the shared session for spec, creating it on first use and
// resetting it when the credential params changed.
func (m *Manager) Session(spec Spec) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[spec.Name]; session != nil {
		session.reconcileSeed(spec.ParamsSeed)
		return session
	}
	session := newSession(fileStore{dir: m.dir}, spec)
	m.sessions[spec.Name] = session
	return session
}
