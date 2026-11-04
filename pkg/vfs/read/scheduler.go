package read

import "github.com/yinzhenyu/qrypt/pkg/readbudget"

// The read domain uses the shared budget implementation so direct reads can
// reuse it without importing pkg/vfs.

type Priority = readbudget.Priority

const (
	PriorityLow    = readbudget.PriorityLow
	PriorityNormal = readbudget.PriorityNormal
	PriorityHigh   = readbudget.PriorityHigh
)

type LoadKind = readbudget.LoadKind

const (
	LoadSlot = readbudget.LoadSlot
	LoadFile = readbudget.LoadFile
)

type Request = readbudget.Request
type Scheduler = readbudget.Scheduler
type SchedulerConfig = readbudget.SchedulerConfig
type SchedulerStats = readbudget.SchedulerStats

func NewScheduler(cfg SchedulerConfig) *Scheduler {
	return readbudget.NewScheduler(cfg)
}

func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		MaxConcurrency: MaxConcurrency,
		HighReserve:    HighReserve,
		PrefetchLimit:  PrefetchLimit,
	}
}
