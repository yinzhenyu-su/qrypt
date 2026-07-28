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
