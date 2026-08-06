package vfs

import (
	"sync"
	"time"
)

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
