package vfs

import (
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/upload"
)

// uploadScheduleState tracks the debounce timers for pending uploads.
// Owned by the upload domain (UploadService.schedule); mu guards timers.
// Lifecycle: created in newUploadState, timers are stopped by Close
// (VFS shutdown) or when an upload is cancelled/rescheduled.
type uploadScheduleState struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
}

func newUploadScheduleState() *uploadScheduleState {
	return &uploadScheduleState{
		timers: map[string]*time.Timer{},
	}
}

type uploadDebugState struct {
	mu      sync.Mutex
	active  map[string]*uploadSnapshotState
	history []UploadSnapshot
}

func newUploadDebugState() *uploadDebugState {
	return &uploadDebugState{
		active: map[string]*uploadSnapshotState{},
	}
}

func (d *uploadDebugState) removeHistoryByID(id string) bool {
	if id == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, upload := range d.history {
		if upload.OpID != id {
			continue
		}
		copy(d.history[i:], d.history[i+1:])
		d.history = d.history[:len(d.history)-1]
		return true
	}
	return false
}

// uploadFaultState registers debug-injected upload cancel faults. Owned
// by the upload domain (UploadService.faults); mu guards cancelFaults.
// Lifecycle: created in newUploadState, entries are added/removed by
// debug commands.
type uploadFaultState struct {
	mu           sync.Mutex
	cancelFaults map[string]*debugUploadCancelFault
}

func newUploadFaultState() *uploadFaultState {
	return &uploadFaultState{
		cancelFaults: map[string]*debugUploadCancelFault{},
	}
}

// UploadService groups the VFS upload-domain state: the persistent store,
// the pending queue, the debounce scheduler, debug snapshots, fault
// injection, hash tracking and admission. Owned by the upload engine and
// workers; initialized together in New.
type UploadService struct {
	store    *uploadStore
	queue    chan PendingUpload
	schedule *uploadScheduleState
	debug    *uploadDebugState
	faults   *uploadFaultState
	hashes   *uploadHashTrackerState
	admit    uploadAdmission
	delay    time.Duration
	workers  int
	done     chan struct{}
}

// newUploadState builds the upload domain state together. store persists
// pending records and staging ownership; queue is the worker inbox.
func newUploadService(store *uploadStore, opts Options, done chan struct{}) *UploadService {
	return &UploadService{
		store:    store,
		queue:    make(chan PendingUpload, 128),
		schedule: newUploadScheduleState(),
		debug:    newUploadDebugState(),
		faults:   newUploadFaultState(),
		hashes:   newUploadHashTrackerState(),
		delay:    opts.UploadDelay,
		workers:  opts.UploadWorkers,
		done:     done,
	}
}

// stopAll stops every debounce timer so no delayed upload fires after
// shutdown. Used by UploadService.Close and the upload scheduler.
func (s *uploadScheduleState) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for path, timer := range s.timers {
		timer.Stop()
		delete(s.timers, path)
	}
}

// Close stops the upload debounce timers. Called by the VFS lifecycle;
// worker goroutines exit on the VFS context themselves.
func (u *UploadService) Close() {
	u.schedule.stopAll()
}

// SetDone wires the lifecycle done channel so that blocking enqueue
// goroutines exit on shutdown. Called by VFS.Start.
func (u *UploadService) SetDone(done chan struct{}) {
	u.done = done
}

// DebugStateByID returns the upload state string for the given op ID.
func (u *UploadService) DebugStateByID(id string) (string, bool) {
	u.debug.mu.Lock()
	defer u.debug.mu.Unlock()
	for _, state := range u.debug.active {
		if state.upload.OpID == id {
			return state.upload.State, true
		}
	}
	for _, snap := range u.debug.history {
		if snap.OpID == id {
			return snap.State, true
		}
	}
	return "", false
}

// DebugActivePathByID returns the path for an active upload with the given op ID.
func (u *UploadService) DebugActivePathByID(id string) (string, bool) {
	u.debug.mu.Lock()
	defer u.debug.mu.Unlock()
	for path, state := range u.debug.active {
		if state.upload.OpID == id {
			return path, true
		}
	}
	return "", false
}

// --- Store accessors ---

func (s *UploadService) SaveUpload(pending PendingUpload) error { return s.store.SaveUpload(pending) }
func (s *UploadService) SaveUploadExact(pending PendingUpload) error {
	return s.store.SaveUploadExact(pending)
}
func (s *UploadService) UploadByPath(path string) (PendingUpload, bool) {
	return s.store.UploadByPath(path)
}
func (s *UploadService) PendingByID(id string) (PendingUpload, bool) { return s.store.PendingByID(id) }
func (s *UploadService) PendingUploads() []PendingUpload             { return s.store.PendingUploads() }
func (s *UploadService) RemoveUpload(path string) error              { return s.store.RemoveUpload(path) }
func (s *UploadService) RemoveUploadsUnder(path string) error {
	return s.store.RemoveUploadsUnder(path)
}
func (s *UploadService) RenameUpload(oldPath string, p PendingUpload) error {
	return s.store.RenameUpload(oldPath, p)
}
func (s *UploadService) RemoveStagingIfUnreferenced(localPath string) {
	s.store.removeStagingIfUnreferenced(localPath)
}

// --- Hash tracker ---

func (s *UploadService) HashRemovePath(path string)  { s.hashes.removePath(path) }
func (s *UploadService) HashRemoveUnder(path string) { s.hashes.removeUnder(path) }
func (s *UploadService) HashRenamePath(oldPath, newPath string, p PendingUpload) {
	s.hashes.renamePath(oldPath, newPath, p)
}
func (s *UploadService) HashStore() *uploadHashTrackerState { return s.hashes }

// --- Admission ---

func (s *UploadService) TryAcquire(p PendingUpload, workers int) bool {
	return s.admit.tryAcquire(p, workers)
}
func (s *UploadService) Release(p PendingUpload) { s.admit.release(p) }

// --- Queue access ---

func (s *UploadService) Queue() chan PendingUpload { return s.queue }
func (s *UploadService) WorkerCount() int          { return s.workers }
func (s *UploadService) Store() *uploadStore       { return s.store }

// --- Fault injection ---

func (s *UploadService) FaultsInject(fault *debugUploadCancelFault) {
	s.faults.mu.Lock()
	defer s.faults.mu.Unlock()
	if s.faults.cancelFaults == nil {
		s.faults.cancelFaults = map[string]*debugUploadCancelFault{}
	}
	s.faults.cancelFaults[fault.id] = fault
}

func (s *UploadService) FaultsRemove(id string) {
	s.faults.mu.Lock()
	defer s.faults.mu.Unlock()
	if s.faults.cancelFaults == nil {
		s.faults.cancelFaults = map[string]*debugUploadCancelFault{}
	}
	delete(s.faults.cancelFaults, id)
}

func (s *UploadService) Faults() *uploadFaultState     { return s.faults }
func (s *UploadService) DebugStore() *uploadDebugState { return s.debug }

// Retry resets a failed pending upload so it re-enters the queue immediately.
func (s *UploadService) Retry(pending PendingUpload) error {
	now := timeutil.Now()
	if qw := s.quietWindow(pending); qw > 0 {
		pending.UpdatedAt = now.Add(-qw - time.Nanosecond).UnixNano()
	} else {
		pending.UpdatedAt = now.UnixNano()
	}
	pending.PermanentFail = false
	pending.LastError = ""
	pending.NextAttemptAt = 0
	if err := s.SaveUploadExact(pending); err != nil {
		return err
	}
	if latest, ok := s.UploadByPath(pending.Path); ok {
		pending = latest
	}
	s.CancelUpload(pending.Path)
	s.EnqueueAfter(pending, 0)
	return nil
}

// RemoveHistoryByID removes a completed upload from debug history.
func (s *UploadService) RemoveHistoryByID(id string) bool {
	return s.debug.removeHistoryByID(id)
}

// Compile-time interface satisfaction check.
var _ upload.Service = (*UploadService)(nil)
