package read

import (
	"context"
	"sync"
	"sync/atomic"
)

// LoadKind selects which budget a Scheduler request draws from. Parallel
// windows acquire a file budget and a slot; foreground and prefetch reads
// acquire a slot only.
type LoadKind int

const (
	LoadSlot LoadKind = iota
	LoadFile
)

// SchedulerConfig is the fixed capacity of one scheduler instance. VFS and
// direct reads use the same implementation with separate instances.
type SchedulerConfig struct {
	MaxConcurrency int
	HighReserve    int
	PrefetchLimit  int
}

func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		MaxConcurrency: MaxConcurrency,
		HighReserve:    HighReserve,
		PrefetchLimit:  PrefetchLimit,
	}
}

// Scheduler owns every read concurrency budget: global normal/high slots,
// per-file parallel permits, prefetch permits, and the foreground-read count.
type Scheduler struct {
	normal   chan struct{}
	high     chan struct{}
	prefetch chan struct{}

	foreground atomic.Int32

	mu          sync.Mutex
	files       map[string]*fileBudget
	nonParallel map[string]struct{}
}

type fileBudget struct {
	ch    chan struct{}
	limit int
	refs  int
}

// Request describes one acquisition. Kind LoadFile uses FileKey/PerFileLimit;
// LoadSlot uses Priority.
type Request struct {
	Kind         LoadKind
	Priority     Priority
	FileKey      string
	PerFileLimit int
}

// SchedulerStats is a snapshot for tests and diagnostics.
type SchedulerStats struct {
	NormalInUse      int
	NormalCapacity   int
	HighInUse        int
	HighCapacity     int
	PrefetchInUse    int
	PrefetchCapacity int
	Files            int
	Foreground       int
}

func NewScheduler(cfg SchedulerConfig) *Scheduler {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = MaxConcurrency
	}
	if cfg.HighReserve < 0 {
		cfg.HighReserve = 0
	}
	if cfg.HighReserve >= cfg.MaxConcurrency {
		cfg.HighReserve = cfg.MaxConcurrency - 1
	}
	if cfg.PrefetchLimit < 1 {
		cfg.PrefetchLimit = 1
	}
	return &Scheduler{
		normal:      make(chan struct{}, cfg.MaxConcurrency-cfg.HighReserve),
		high:        make(chan struct{}, cfg.HighReserve),
		prefetch:    make(chan struct{}, cfg.PrefetchLimit),
		files:       map[string]*fileBudget{},
		nonParallel: map[string]struct{}{},
	}
}

// Acquire dispatches to the file or slot budget. Parallel callers acquire a
// file permit first, then a slot, matching the existing lock order.
func (s *Scheduler) Acquire(ctx context.Context, req Request) (func(), error) {
	if req.Kind == LoadFile {
		return s.AcquireFile(ctx, req.FileKey, req.PerFileLimit)
	}
	return s.AcquireSlot(ctx, req.Priority)
}

// AcquireSlot takes one global slot. High priority tries the normal pool
// first, then the reserve; normal and low priority use the normal pool.
func (s *Scheduler) AcquireSlot(ctx context.Context, prio Priority) (func(), error) {
	if prio == PriorityHigh {
		select {
		case s.normal <- struct{}{}:
			return func() { <-s.normal }, nil
		default:
		}
		select {
		case s.high <- struct{}{}:
			return func() { <-s.high }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	select {
	case s.normal <- struct{}{}:
		return func() { <-s.normal }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// AcquireFile takes one permit from the file's stable budget. The first
// caller fixes the limit; later windows with a different requested stream
// count reuse it instead of replacing a live channel.
func (s *Scheduler) AcquireFile(ctx context.Context, key string, limit int) (func(), error) {
	if limit < 1 {
		limit = 1
	}
	s.mu.Lock()
	file := s.files[key]
	if file == nil {
		file = &fileBudget{ch: make(chan struct{}, limit), limit: limit}
		s.files[key] = file
	}
	file.refs++
	s.mu.Unlock()

	release := func() {
		<-file.ch
		s.mu.Lock()
		file.refs--
		if file.refs == 0 && s.files[key] == file {
			delete(s.files, key)
		}
		s.mu.Unlock()
	}
	select {
	case file.ch <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		s.mu.Lock()
		file.refs--
		if file.refs == 0 && s.files[key] == file {
			delete(s.files, key)
		}
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (s *Scheduler) TryAcquirePrefetch() bool {
	select {
	case s.prefetch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Scheduler) ReleasePrefetch() { <-s.prefetch }

func (s *Scheduler) BeginForeground() { s.foreground.Add(1) }
func (s *Scheduler) EndForeground()   { s.foreground.Add(-1) }
func (s *Scheduler) ForegroundCount() int {
	return int(s.foreground.Load())
}

func (s *Scheduler) MarkNonParallel(key string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	s.nonParallel[key] = struct{}{}
	s.mu.Unlock()
}

func (s *Scheduler) IsNonParallel(key string) bool {
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.nonParallel[key]
	return ok
}

func (s *Scheduler) PerFileInUse(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	file := s.files[key]
	if file == nil {
		return 0
	}
	return len(file.ch)
}

func (s *Scheduler) Stats() SchedulerStats {
	s.mu.Lock()
	files := len(s.files)
	s.mu.Unlock()
	return SchedulerStats{
		NormalInUse:      len(s.normal),
		NormalCapacity:   cap(s.normal),
		HighInUse:        len(s.high),
		HighCapacity:     cap(s.high),
		PrefetchInUse:    len(s.prefetch),
		PrefetchCapacity: cap(s.prefetch),
		Files:            files,
		Foreground:       int(s.foreground.Load()),
	}
}
