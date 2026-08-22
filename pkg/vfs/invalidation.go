package vfs

import (
	"strings"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/logging"
)

type invalidationState struct {
	mu        sync.RWMutex
	nextID    uint64
	listeners map[uint64]func(string)
}

func (s *invalidationState) subscribe(listener func(string)) func() {
	if listener == nil {
		return func() {}
	}
	s.mu.Lock()
	if s.listeners == nil {
		s.listeners = make(map[uint64]func(string))
	}
	s.nextID++
	id := s.nextID
	s.listeners[id] = listener
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.listeners, id)
			s.mu.Unlock()
		})
	}
}

func (s *invalidationState) emit(path string) {
	s.mu.RLock()
	listeners := make([]func(string), 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.RUnlock()
	for _, listener := range listeners {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					logging.L.Warnf("[VFS] invalidation listener panicked path=%q panic=%v", path, recovered)
				}
			}()
			listener(path)
		}()
	}
}

func (v *VFS) SubscribeInvalidations(listener func(string)) func() {
	return v.invalidations.subscribe(listener)
}

func (v *VFS) emitInvalidation(path string) {
	v.invalidations.emit(cleanVirtual(path))
}

func (n *Namespace) SubscribeInvalidations(listener func(string)) func() {
	if listener == nil {
		return func() {}
	}
	// Namespace mounts are fixed by NewNamespace, so one subscription snapshot
	// covers the Namespace lifetime.
	n.mu.RLock()
	unsubscribes := make([]func(), 0, len(n.mounts))
	for name, fs := range n.mounts {
		mountName := name
		unsubscribes = append(unsubscribes, fs.SubscribeInvalidations(func(path string) {
			listener(joinVirtual("/"+mountName, strings.TrimPrefix(path, "/")))
		}))
	}
	n.mu.RUnlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			for _, unsubscribe := range unsubscribes {
				unsubscribe()
			}
		})
	}
}
