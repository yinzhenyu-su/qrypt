package vfs

import (
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"strings"
	"sync"
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
	v.invalidations.emit(vfstypes.CleanVirtualPath(path))
}

// invalidationSubscription tracks one Namespace-level invalidation listener
// across dynamically added/removed mounts. The mount set is no longer fixed
// at construction (AddMount/RemoveMount), so a subscription cannot be a
// one-time snapshot: attach/detach track the mount set for the listener's
// lifetime. attach/detach run under n.mu (read/write respectively).
type invalidationSubscription struct {
	active   bool
	unsubs   map[string]func()
	listener func(string)
}

// attach subscribes the listener to one mount. The caller must hold n.mu.
func (s *invalidationSubscription) attach(name string, fs *VFS) {
	if !s.active {
		return
	}
	mountName := name
	s.unsubs[name] = fs.SubscribeInvalidations(func(path string) {
		s.listener(vfstypes.JoinVirtualPath("/"+mountName, strings.TrimPrefix(path, "/")))
	})
}

// detach unsubscribes one mount. The caller must hold n.mu.
func (s *invalidationSubscription) detach(name string) {
	if unsub, ok := s.unsubs[name]; ok {
		delete(s.unsubs, name)
		unsub()
	}
}

func (n *Namespace) SubscribeInvalidations(listener func(string)) func() {
	if listener == nil {
		return func() {}
	}
	var once sync.Once
	sub := &invalidationSubscription{active: true, unsubs: map[string]func(){}, listener: listener}
	n.mu.RLock()
	n.subs[sub] = struct{}{}
	for name, fs := range n.mounts {
		sub.attach(name, fs)
	}
	n.mu.RUnlock()
	return func() {
		once.Do(func() {
			n.mu.Lock()
			defer n.mu.Unlock()
			if !sub.active {
				delete(n.subs, sub)
				return
			}
			sub.active = false
			for _, unsub := range sub.unsubs {
				unsub()
			}
			sub.unsubs = nil
			delete(n.subs, sub)
		})
	}
}
