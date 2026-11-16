// Package readbudget owns read admission. A scheduler grants all the resources
// for one request together; a queued request never holds a partial permit.
package readbudget

import (
	"context"
	"sync"
	"sync/atomic"
)

type Priority int

const (
	PriorityLow Priority = iota
	PriorityNormal
	PriorityHigh
)

type SchedulerConfig struct{ MaxConcurrency, HighReserve, PrefetchLimit int }
type request struct {
	Priority     Priority
	FileKey      string
	PerFileLimit int
}
type SchedulerStats struct {
	NormalInUse, NormalCapacity, HighInUse, HighCapacity int
	PrefetchInUse, PrefetchCapacity, Files, Foreground   int
}
type fileBudget struct{ used, limit int }
type waiter struct {
	req     request
	ready   chan struct{}
	release func()
	granted bool
}
type Scheduler struct {
	mu                              sync.Mutex
	normal, high, prefetch          int
	normalCap, highCap, prefetchCap int
	files                           map[string]*fileBudget
	nonParallel                     map[string]struct{}
	waiting                         []*waiter
	foreground                      atomic.Int32
	changed                         chan struct{}
}

func NewScheduler(c SchedulerConfig) *Scheduler {
	if c.MaxConcurrency <= 0 {
		c.MaxConcurrency = 32
	}
	c.HighReserve = max(0, min(c.HighReserve, c.MaxConcurrency-1))
	c.PrefetchLimit = max(1, c.PrefetchLimit)
	return &Scheduler{normalCap: c.MaxConcurrency - c.HighReserve, highCap: c.HighReserve, prefetchCap: c.PrefetchLimit, files: map[string]*fileBudget{}, nonParallel: map[string]struct{}{}, changed: make(chan struct{})}
}
func (s *Scheduler) fits(r request) bool {
	if s.normal >= s.normalCap && (r.Priority != PriorityHigh || s.high >= s.highCap) {
		return false
	}
	if f := s.files[r.FileKey]; f != nil && f.used >= f.limit {
		return false
	}
	return true
}

// take runs under mu after both capacities have been checked.
func (s *Scheduler) take(r request) func() {
	high := s.normal >= s.normalCap
	if high {
		s.high++
	} else {
		s.normal++
	}
	f := s.files[r.FileKey]
	if f == nil {
		f = &fileBudget{limit: max(1, r.PerFileLimit)}
		s.files[r.FileKey] = f
	}
	f.used++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if high {
				s.high--
			} else {
				s.normal--
			}
			f := s.files[r.FileKey]
			f.used--
			if f.used == 0 {
				delete(s.files, r.FileKey)
			}
			s.dispatch()
			s.notifyCapacity()
		})
	}
}

// dispatch grants the highest-priority eligible waiter first. A blocked file
// does not prevent another file using a free slot.
func (s *Scheduler) dispatch() {
	for {
		chosen := -1
		for i, w := range s.waiting {
			if s.fits(w.req) && (chosen < 0 || w.req.Priority > s.waiting[chosen].req.Priority) {
				chosen = i
			}
		}
		if chosen < 0 {
			return
		}
		w := s.waiting[chosen]
		s.waiting = append(s.waiting[:chosen], s.waiting[chosen+1:]...)
		w.release = s.take(w.req)
		w.granted = true
		close(w.ready)
	}
}
func (s *Scheduler) acquire(ctx context.Context, r request) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w := &waiter{req: r, ready: make(chan struct{})}
	s.mu.Lock()
	s.waiting = append(s.waiting, w)
	s.dispatch()
	s.mu.Unlock()
	select {
	case <-w.ready:
		if err := ctx.Err(); err != nil {
			w.release()
			return nil, err
		}
		return w.release, nil
	case <-ctx.Done():
		s.mu.Lock()
		granted := w.granted
		if !granted {
			for i, c := range s.waiting {
				if c == w {
					s.waiting = append(s.waiting[:i], s.waiting[i+1:]...)
					break
				}
			}
			s.dispatch()
		}
		s.mu.Unlock()
		if granted {
			w.release()
		}
		return nil, ctx.Err()
	}
}
func (s *Scheduler) tryAcquire(r request) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.fits(r) {
		return nil, false
	}
	for _, w := range s.waiting {
		if w.req.Priority >= r.Priority && s.fits(w.req) {
			return nil, false
		}
	}
	return s.take(r), true
}
func (s *Scheduler) AcquireRange(ctx context.Context, key string, limit int, p Priority) (func(), error) {
	return s.acquire(ctx, request{FileKey: key, PerFileLimit: limit, Priority: p})
}
func (s *Scheduler) TryAcquireRange(key string, limit int, p Priority) (func(), bool) {
	return s.tryAcquire(request{FileKey: key, PerFileLimit: limit, Priority: p})
}
func (s *Scheduler) TryAcquirePrefetch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prefetch >= s.prefetchCap {
		return false
	}
	s.prefetch++
	return true
}

// CapacityChanged returns a broadcast notification for released capacity.
// Subscribe before attempting nonblocking admission to avoid a lost wakeup.
func (s *Scheduler) CapacityChanged() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

func (s *Scheduler) notifyCapacity() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Scheduler) ReleasePrefetch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefetch--
	s.notifyCapacity()
}
func (s *Scheduler) BeginForeground()     { s.foreground.Add(1) }
func (s *Scheduler) EndForeground()       { s.foreground.Add(-1) }
func (s *Scheduler) ForegroundCount() int { return int(s.foreground.Load()) }
func (s *Scheduler) MarkNonParallel(key string) {
	if key != "" {
		s.mu.Lock()
		s.nonParallel[key] = struct{}{}
		s.mu.Unlock()
	}
}
func (s *Scheduler) IsNonParallel(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.nonParallel[key]
	return key != "" && ok
}
func (s *Scheduler) PerFileInUse(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.files[key]; f != nil {
		return f.used
	}
	return 0
}
func (s *Scheduler) GrantStreams(want int, key string, limit int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" || limit < 1 {
		return max(1, want)
	}
	if f := s.files[key]; f != nil {
		limit = min(limit, f.limit)
	}
	return max(1, min(want, limit))
}
func (s *Scheduler) Stats() SchedulerStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SchedulerStats{NormalInUse: s.normal, NormalCapacity: s.normalCap, HighInUse: s.high, HighCapacity: s.highCap, PrefetchInUse: s.prefetch, PrefetchCapacity: s.prefetchCap, Files: len(s.files), Foreground: int(s.foreground.Load())}
}
