package mount

import (
	"path"
	"runtime"
	"sync"

	"github.com/winfsp/cgofuse/fuse"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// invalidationForwarder keeps the VFS upload commit path independent from a
// potentially slow kernel notification. One worker preserves notification
// ordering; close waits only for a notification already inside FUSE and drops
// queued work because the host is about to be unmounted.
type invalidationForwarder struct {
	notify func(string, uint32) bool

	mu      sync.Mutex
	cond    *sync.Cond
	queue   []string
	pending map[string]struct{}
	closed  bool
	done    chan struct{}
}

func newInvalidationForwarder(notify func(string, uint32) bool) *invalidationForwarder {
	f := &invalidationForwarder{
		notify:  notify,
		pending: make(map[string]struct{}),
		done:    make(chan struct{}),
	}
	f.cond = sync.NewCond(&f.mu)
	go f.run()
	return f
}

func (f *invalidationForwarder) enqueue(filePath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	for _, invalidationPath := range mountInvalidationPaths(filePath, runtime.GOOS) {
		if _, exists := f.pending[invalidationPath]; exists {
			continue
		}
		f.pending[invalidationPath] = struct{}{}
		f.queue = append(f.queue, invalidationPath)
	}
	f.cond.Signal()
}

func (f *invalidationForwarder) run() {
	defer close(f.done)
	for {
		f.mu.Lock()
		for !f.closed && len(f.queue) == 0 {
			f.cond.Wait()
		}
		if f.closed {
			f.mu.Unlock()
			return
		}
		invalidationPath := f.queue[0]
		f.queue[0] = ""
		f.queue = f.queue[1:]
		if len(f.queue) == 0 {
			f.queue = nil
		}
		delete(f.pending, invalidationPath)
		f.mu.Unlock()

		notified := f.notify(invalidationPath, fuse.NOTIFY_CREATE|fuse.NOTIFY_TRUNCATE)
		logging.L.Debugf("[FUSE] invalidate path=%q notified=%t", invalidationPath, notified)
	}
}

func (f *invalidationForwarder) close() {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		f.queue = nil
		clear(f.pending)
		f.cond.Broadcast()
	}
	f.mu.Unlock()
	<-f.done
}

func subscribeInvalidations(fs vfs.FileSystem, notify func(string, uint32) bool) func() {
	source, ok := fs.(vfs.InvalidationSource)
	if !ok || notify == nil {
		return func() {}
	}

	forwarder := newInvalidationForwarder(notify)
	unsubscribe := source.SubscribeInvalidations(forwarder.enqueue)
	var once sync.Once
	return func() {
		once.Do(func() {
			unsubscribe()
			forwarder.close()
		})
	}
}

func mountInvalidationPaths(filePath, goos string) []string {
	if goos == "windows" {
		return []string{filePath}
	}
	parent := path.Dir(filePath)
	if parent == filePath {
		return []string{filePath}
	}
	return []string{filePath, parent}
}
