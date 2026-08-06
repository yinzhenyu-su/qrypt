package vfs

import (
	"sync"
	"time"
)

// uploadScheduleState tracks the debounce timers for pending uploads.
// Owned by the upload domain (uploadState.schedule); mu guards timers.
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

// uploadFaultState registers debug-injected upload cancel faults. Owned
// by the upload domain (uploadState.faults); mu guards cancelFaults.
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

// uploadState groups the VFS upload-domain state: the persistent store,
// the pending queue, the debounce scheduler, debug snapshots, fault
// injection, hash tracking and admission. Owned by the upload engine and
// workers; initialized together in New.
type uploadState struct {
	store    *uploadStore
	queue    chan PendingUpload
	schedule *uploadScheduleState
	debug    *uploadDebugState
	faults   *uploadFaultState
	hashes   *uploadHashTrackerState
	admit    uploadAdmission
	delay    time.Duration
	workers  int
}

// newUploadState builds the upload domain state together. store persists
// pending records and staging ownership; queue is the worker inbox.
func newUploadState(store *uploadStore, opts Options) *uploadState {
	return &uploadState{
		store:    store,
		queue:    make(chan PendingUpload, 128),
		schedule: newUploadScheduleState(),
		debug:    newUploadDebugState(),
		faults:   newUploadFaultState(),
		hashes:   newUploadHashTrackerState(),
		delay:    opts.UploadDelay,
		workers:  opts.UploadWorkers,
	}
}

// stopAll stops every debounce timer so no delayed upload fires after
// shutdown. Used by uploadState.Close and the upload scheduler.
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
func (u *uploadState) Close() {
	u.schedule.stopAll()
}
