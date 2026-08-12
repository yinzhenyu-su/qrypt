package task

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Source interface {
	ListTasks(ctx context.Context, filter Filter) ([]Task, error)
	GetTask(ctx context.Context, id string) (Task, error)
	CancelTask(ctx context.Context, id string) error
	RetryTask(ctx context.Context, id string) error
}

type dismissibleSource interface {
	DismissTask(ctx context.Context, id string) error
	DismissFinishedTasks(ctx context.Context, filter Filter) (int, error)
}

type RunFunc func(ctx context.Context, update UpdateFunc) error

type UpdateFunc func(func(*Task))

type Manager struct {
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	store       Store
	sources     []Source
	eventSeq    uint64
	nextSubID   uint64
	subscribers map[uint64]subscriber
	wg          sync.WaitGroup
}

type subscriber struct {
	filter Filter
	events chan Event
}

func NewManager(sources ...Source) *Manager {
	return NewManagerWithStore(NewMemoryStore(), sources...)
}

func NewManagerWithStore(store Store, sources ...Source) *Manager {
	if store == nil {
		store = NewMemoryStore()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		ctx:         ctx,
		cancel:      cancel,
		store:       store,
		sources:     append([]Source(nil), sources...),
		subscribers: map[uint64]subscriber{},
	}
}

// PersistenceError reports whether the manager's store has lost durability.
// Task execution results remain available in memory; callers must not treat
// this as proof that the underlying business operation failed.
func (m *Manager) PersistenceError() error {
	if m == nil || m.store == nil {
		return nil
	}
	health, ok := m.store.(PersistenceHealth)
	if !ok {
		return nil
	}
	return health.PersistenceError()
}

func (m *Manager) AddSource(source Source) {
	if source == nil {
		return
	}
	m.mu.Lock()
	m.sources = append(m.sources, source)
	m.mu.Unlock()
}

func (m *Manager) Submit(ctx context.Context, item Task, run RunFunc) Task {
	if err := ctx.Err(); err != nil {
		item.State = StateCanceled
		item.Error = &Error{Message: err.Error()}
		return item
	}
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	if item.State == "" {
		item.State = StateQueued
	}
	normalizeManagedTaskCapabilities(&item)
	runCtx, cancel := context.WithCancel(m.ctx)

	item = m.store.PutManaged(ManagedTask{Task: item, Run: run, Cancel: cancel})
	m.broadcastTaskUpdated(item)

	m.wg.Add(1)
	go m.run(runCtx, item.ID)
	return item
}

func (m *Manager) run(ctx context.Context, id string) {
	defer m.wg.Done()
	m.update(id, func(item *Task) {
		item.State = StateRunning
		item.StartedAt = time.Now().UTC()
		item.UpdatedAt = item.StartedAt
	})
	run := m.runFunc(id)
	if run == nil {
		return
	}
	err := run(ctx, func(fn func(*Task)) {
		m.update(id, fn)
	})
	m.update(id, func(item *Task) {
		now := time.Now().UTC()
		item.UpdatedAt = now
		item.CompletedAt = now
		item.Capabilities.Cancelable = false
		item.Capabilities.Dismissible = true
		if err != nil {
			if ctx.Err() != nil {
				item.State = StateCanceled
				item.Error = &Error{Message: ctx.Err().Error()}
				return
			}
			item.State = StateFailed
			item.Error = &Error{Message: err.Error(), Retryable: item.Capabilities.Retryable}
			return
		}
		if item.State == StatePartialFailed {
			return
		}
		item.State = StateSucceeded
		item.Error = nil
	})
}

func (m *Manager) runFunc(id string) RunFunc {
	if managed, ok := m.store.GetManaged(id); ok {
		return managed.Run
	}
	return nil
}

func normalizeManagedTaskCapabilities(item *Task) {
	if item == nil {
		return
	}
	if isTerminalState(item.State) {
		item.Capabilities.Cancelable = false
		item.Capabilities.Dismissible = true
		return
	}
	item.Capabilities.Cancelable = true
	item.Capabilities.Dismissible = false
}

func (m *Manager) update(id string, fn func(*Task)) {
	m.mu.Lock()
	hasSubscribers := len(m.subscribers) > 0
	m.mu.Unlock()
	apply := func(managed *ManagedTask) {
		fn(&managed.Task)
		normalizeManagedTaskCapabilities(&managed.Task)
		managed.Task.UpdatedAt = time.Now().UTC()
		if managed.Task.Detail != nil {
			if phase, ok := managed.Task.Detail["phase"].(string); ok {
				managed.Task.Progress.Phase = phase
			}
		}
	}
	if !hasSubscribers {
		// Fast path: nobody observes updates, so mutate in place without
		// cloning the task for broadcast.
		m.store.UpdateManagedInPlace(id, apply)
		return
	}
	managed, ok := m.store.UpdateManaged(id, apply)
	if ok {
		m.broadcastTaskUpdated(managed.Task)
	}
}

func (m *Manager) ListTasks(ctx context.Context, filter Filter) ([]Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []Task
	sources := m.sourcesSnapshot()
	sourceFilter := filter
	sourceFilter.Limit = 0
	for _, managed := range m.store.ListManaged(filter) {
		out = append(out, managed.Task)
	}
	for _, source := range sources {
		tasks, err := source.ListTasks(ctx, sourceFilter)
		if err != nil {
			return nil, err
		}
		out = append(out, tasks...)
	}
	sortTasks(out)
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (m *Manager) GetTask(ctx context.Context, id string) (Task, error) {
	if id == "" {
		return Task{}, fmt.Errorf("task: id required")
	}
	if err := ctx.Err(); err != nil {
		return Task{}, err
	}
	if managed, ok := m.store.GetManaged(id); ok {
		return managed.Task, nil
	}
	sources := m.sourcesSnapshot()
	for _, source := range sources {
		item, err := source.GetTask(ctx, id)
		if err == nil {
			return item, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Task{}, err
		}
	}
	return Task{}, fmt.Errorf("%w: %q", ErrNotFound, id)
}

func (m *Manager) CancelTask(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("task: id required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if managed, ok := m.store.GetManaged(id); ok {
		if !managed.Task.Capabilities.Cancelable {
			return fmt.Errorf("task: %q is not cancelable", id)
		}
		if managed.Cancel != nil {
			managed.Cancel()
		}
		return nil
	}
	sources := m.sourcesSnapshot()
	for _, source := range sources {
		if err := source.CancelTask(ctx, id); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return fmt.Errorf("%w: %q", ErrNotFound, id)
}

func (m *Manager) RetryTask(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("task: id required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if managed, ok := m.store.GetManaged(id); ok {
		if !managed.Task.Capabilities.Retryable || managed.Run == nil {
			return fmt.Errorf("task: %q is not retryable", id)
		}
		runCtx, cancel := context.WithCancel(m.ctx)
		managed, ok := m.store.UpdateManaged(id, func(managed *ManagedTask) {
			managed.Cancel = cancel
			managed.Task.State = StateQueued
			managed.Task.Error = nil
			managed.Task.RetryCount++
			managed.Task.CompletedAt = time.Time{}
		})
		if ok {
			m.broadcastTaskUpdated(managed.Task)
		}
		m.wg.Add(1)
		go m.run(runCtx, id)
		return nil
	}
	sources := m.sourcesSnapshot()
	for _, source := range sources {
		if err := source.RetryTask(ctx, id); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return fmt.Errorf("%w: %q", ErrNotFound, id)
}

func (m *Manager) DismissTask(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("task: id required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	managed, ok := m.store.GetManaged(id)
	if !ok {
		sources := m.sourcesSnapshot()
		for _, source := range sources {
			dismissible, ok := source.(dismissibleSource)
			if !ok {
				continue
			}
			if err := dismissible.DismissTask(ctx, id); err == nil {
				m.broadcastTaskRemoved(Task{ID: id})
				return nil
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if managed.Task.Capabilities.Cancelable {
		if managed.Cancel != nil {
			managed.Cancel()
		}
		if !m.store.DismissManaged(id) {
			return fmt.Errorf("%w: %q", ErrNotFound, id)
		}
		m.broadcastTaskRemoved(managed.Task)
		return nil
	}
	if !isTerminalState(managed.Task.State) {
		return fmt.Errorf("task: %q is not terminal", id)
	}
	if !managed.Task.Capabilities.Dismissible {
		return fmt.Errorf("task: %q is not dismissible", id)
	}
	if !m.store.DismissManaged(id) {
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	m.broadcastTaskRemoved(managed.Task)
	return nil
}

func (m *Manager) DismissFinishedTasks(ctx context.Context, filter Filter) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	managed := m.store.ListManaged(filter)
	removed := 0
	for _, item := range managed {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if !isTerminalState(item.Task.State) {
			continue
		}
		if !item.Task.Capabilities.Dismissible {
			continue
		}
		if m.store.DismissManaged(item.Task.ID) {
			removed++
			m.broadcastTaskRemoved(item.Task)
		}
	}
	sources := m.sourcesSnapshot()
	for _, source := range sources {
		dismissible, ok := source.(dismissibleSource)
		if !ok {
			continue
		}
		n, err := dismissible.DismissFinishedTasks(ctx, filter)
		if err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

func (m *Manager) Subscribe(filter Filter) *Subscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSubID++
	id := m.nextSubID
	ch := make(chan Event, defaultSubscriptionBuffer)
	m.subscribers[id] = subscriber{filter: filter, events: ch}
	return &Subscription{
		events: ch,
		close: func() {
			m.mu.Lock()
			sub, ok := m.subscribers[id]
			if ok {
				delete(m.subscribers, id)
			}
			m.mu.Unlock()
			if ok {
				close(sub.events)
			}
		},
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.store.ForEachManaged(func(managed ManagedTask) {
		if managed.Cancel != nil {
			managed.Cancel()
		}
	})
	for id, sub := range m.subscribers {
		delete(m.subscribers, id)
		close(sub.events)
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) broadcastTaskUpdated(item Task) {
	m.broadcast(Event{Type: EventTaskUpdated, TaskID: item.ID, Task: taskPtr(cloneTask(item))})
}

func (m *Manager) broadcastTaskRemoved(item Task) {
	m.broadcast(Event{Type: EventTaskRemoved, TaskID: item.ID, Task: taskPtr(cloneTask(item))})
}

func (m *Manager) broadcast(event Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventSeq++
	event.Seq = m.eventSeq
	for _, sub := range m.subscribers {
		if event.Task != nil && !sub.filter.Match(*event.Task) {
			continue
		}
		select {
		case sub.events <- event:
		default:
			select {
			case <-sub.events:
			default:
			}
			select {
			case sub.events <- event:
			default:
			}
		}
	}
}

func taskPtr(item Task) *Task {
	return &item
}

func isTerminalState(state State) bool {
	switch state {
	case StateSucceeded, StatePartialFailed, StateFailed, StateCanceled:
		return true
	default:
		return false
	}
}

func (m *Manager) sourcesSnapshot() []Source {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Source(nil), m.sources...)
}

func sortTasks(tasks []Task) {
	sort.Slice(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		aActive := !isTerminalState(a.State)
		bActive := !isTerminalState(b.State)
		if aActive != bActive {
			return aActive
		}
		aTime := a.CreatedAt
		if aTime.IsZero() {
			aTime = a.StartedAt
		}
		if aTime.IsZero() {
			aTime = a.UpdatedAt
		}
		bTime := b.CreatedAt
		if bTime.IsZero() {
			bTime = b.StartedAt
		}
		if bTime.IsZero() {
			bTime = b.UpdatedAt
		}
		if !aTime.Equal(bTime) {
			return aTime.After(bTime)
		}
		return a.ID < b.ID
	})
}

func cloneTask(item Task) Task {
	if item.Detail != nil {
		detail := make(map[string]any, len(item.Detail))
		for key, value := range item.Detail {
			detail[key] = value
		}
		item.Detail = detail
	}
	if item.Error != nil {
		err := *item.Error
		item.Error = &err
	}
	if item.Result.Items != nil {
		items := make([]ItemResult, len(item.Result.Items))
		for i, result := range item.Result.Items {
			items[i] = result
			if result.Error != nil {
				err := *result.Error
				items[i].Error = &err
			}
		}
		item.Result.Items = items
	}
	return item
}
