package task

import (
	"context"
	"sync"
)

type Store interface {
	PutManaged(ManagedTask) Task
	GetManaged(id string) (ManagedTask, bool)
	UpdateManaged(id string, fn func(*ManagedTask)) (ManagedTask, bool)
	// UpdateManagedInPlace mutates the stored task without cloning. Callers
	// must not retain the pointer passed to fn. It is the no-subscriber fast
	// path for progress updates that no one observes.
	UpdateManagedInPlace(id string, fn func(*ManagedTask)) bool
	DismissManaged(id string) bool
	ListManaged(filter Filter) []ManagedTask
	ForEachManaged(fn func(ManagedTask))
}

type ManagedTask struct {
	Task   Task
	Run    RunFunc
	Cancel context.CancelFunc
}

type MemoryStore struct {
	mu    sync.Mutex
	seq   uint64
	tasks map[string]ManagedTask
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{tasks: map[string]ManagedTask{}}
}

func (s *MemoryStore) LoadTask(item Task) Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.Version > s.seq {
		s.seq = item.Version
	}
	item = cloneTask(item)
	s.tasks[item.ID] = ManagedTask{Task: item}
	return cloneTask(item)
}

func (s *MemoryStore) PutManaged(managed ManagedTask) Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	managed.Task.Version = s.seq
	managed.Task = cloneTask(managed.Task)
	s.tasks[managed.Task.ID] = managed
	return cloneTask(managed.Task)
}

func (s *MemoryStore) GetManaged(id string) (ManagedTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	managed, ok := s.tasks[id]
	if !ok {
		return ManagedTask{}, false
	}
	managed.Task = cloneTask(managed.Task)
	return managed, true
}

func (s *MemoryStore) UpdateManaged(id string, fn func(*ManagedTask)) (ManagedTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	managed, ok := s.tasks[id]
	if !ok {
		return ManagedTask{}, false
	}
	fn(&managed)
	s.seq++
	managed.Task.Version = s.seq
	managed.Task = cloneTask(managed.Task)
	s.tasks[id] = managed
	return ManagedTask{Task: cloneTask(managed.Task), Run: managed.Run, Cancel: managed.Cancel}, true
}

func (s *MemoryStore) UpdateManagedInPlace(id string, fn func(*ManagedTask)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	managed, ok := s.tasks[id]
	if !ok {
		return false
	}
	fn(&managed)
	s.seq++
	managed.Task.Version = s.seq
	s.tasks[id] = managed
	return true
}

func (s *MemoryStore) DismissManaged(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return false
	}
	delete(s.tasks, id)
	return true
}

func (s *MemoryStore) ListManaged(filter Filter) []ManagedTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ManagedTask, 0, len(s.tasks))
	for _, managed := range s.tasks {
		item := cloneTask(managed.Task)
		if filter.Match(item) {
			managed.Task = item
			out = append(out, managed)
		}
	}
	return out
}

func (s *MemoryStore) ForEachManaged(fn func(ManagedTask)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, managed := range s.tasks {
		managed.Task = cloneTask(managed.Task)
		fn(managed)
	}
}
