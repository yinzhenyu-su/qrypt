package task

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestManagerSubmitDetachesFromSubmitContext(t *testing.T) {
	m := NewManager()
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	item := m.Submit(ctx, Task{ID: "move-1", Type: TypeMoveRemote, Capabilities: Capabilities{Cancelable: true}}, func(ctx context.Context, update UpdateFunc) error {
		cancel()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if item.ID != "move-1" {
		t.Fatalf("submitted task = %+v", item)
	}
	close(done)
	got := waitTaskState(t, m, "move-1", StateSucceeded)
	if got.State != StateSucceeded {
		t.Fatalf("task state = %s, want succeeded", got.State)
	}
}

func TestManagerDerivesTaskActionsFromState(t *testing.T) {
	m := NewManager()
	defer m.Close()
	item, err := m.SubmitIdempotent(context.Background(), Task{ID: "task-1"}, func(context.Context, UpdateFunc) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Capabilities.Actions) != 1 || item.Capabilities.Actions[0] != ActionCancel {
		t.Fatalf("queued actions = %v, want cancel", item.Capabilities.Actions)
	}
	got := waitTaskState(t, m, "task-1", StateSucceeded)
	if len(got.Capabilities.Actions) != 1 || got.Capabilities.Actions[0] != ActionDismiss {
		t.Fatalf("terminal actions = %v, want dismiss", got.Capabilities.Actions)
	}
}

func TestManagerListsOwnAndSourceTasks(t *testing.T) {
	source := staticSource{tasks: []Task{{ID: "upload-1", Type: TypeUploadRemote, State: StateRunning}}}
	m := NewManager(source)
	defer m.Close()
	m.Submit(context.Background(), Task{ID: "move-1", Type: TypeMoveRemote}, func(context.Context, UpdateFunc) error {
		return nil
	})
	waitTaskState(t, m, "move-1", StateSucceeded)

	tasks, err := m.ListTasks(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %+v, want own and source task", tasks)
	}
}

func TestManagerSubscribeFromReplaysEvents(t *testing.T) {
	m := NewManager()
	defer m.Close()
	m.broadcast(Event{Type: EventTaskUpdated, TaskID: "task-1"})
	m.broadcast(Event{Type: EventTaskUpdated, TaskID: "task-2"})

	sub := m.SubscribeFrom(Filter{}, 1)
	defer sub.Close()
	events, err := sub.ReadAvailable()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].TaskID != "task-2" || events[0].Seq != 2 {
		t.Fatalf("replayed events = %+v, want task-2 seq 2", events)
	}
}

func TestManagerSubscribeFromSignalsHistoryGap(t *testing.T) {
	m := NewManager()
	defer m.Close()
	for i := 0; i < eventHistoryLimit+2; i++ {
		m.broadcast(Event{Type: EventTaskUpdated, TaskID: "task"})
	}

	sub := m.SubscribeFrom(Filter{}, 1)
	defer sub.Close()
	events, err := sub.ReadAvailable()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Type != EventTaskGap || !events[0].SnapshotRequired {
		t.Fatalf("gap event = %+v, want snapshot-required gap", events)
	}
}

func TestManagerAppliesLimitAfterMergingSources(t *testing.T) {
	now := time.Now()
	source := staticSource{tasks: []Task{
		{ID: "upload-old", Type: TypeUploadRemote, State: StateRunning, UpdatedAt: now.Add(-time.Hour)},
	}}
	m := NewManager(source)
	defer m.Close()
	m.Submit(context.Background(), Task{ID: "move-new", Type: TypeMoveRemote, UpdatedAt: now}, func(context.Context, UpdateFunc) error {
		return nil
	})
	waitTaskState(t, m, "move-new", StateSucceeded)

	tasks, err := m.ListTasks(context.Background(), Filter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "upload-old" {
		t.Fatalf("limited tasks = %+v, want active task first", tasks)
	}
}

func TestManagerCancelOwnTask(t *testing.T) {
	m := NewManager()
	defer m.Close()
	entered := make(chan struct{})
	m.Submit(context.Background(), Task{ID: "download-1", Type: TypeDownload, Capabilities: Capabilities{Cancelable: true}}, func(ctx context.Context, update UpdateFunc) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	<-entered
	if err := m.CancelTask(context.Background(), "download-1"); err != nil {
		t.Fatal(err)
	}
	got := waitTaskState(t, m, "download-1", StateCanceled)
	if got.State != StateCanceled {
		t.Fatalf("task state = %s, want canceled", got.State)
	}
}

func TestManagerDismissTaskCancelsActiveOrRemovesTerminalTask(t *testing.T) {
	m := NewManager()
	defer m.Close()
	entered := make(chan struct{})
	canceled := make(chan struct{})
	m.Submit(context.Background(), Task{
		ID:    "running",
		Type:  TypeDeleteBatch,
		State: StateRunning,
		Capabilities: Capabilities{
			Cancelable: true,
		},
	}, func(ctx context.Context, update UpdateFunc) error {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	})
	<-entered
	if err := m.DismissTask(context.Background(), "running"); err != nil {
		t.Fatal(err)
	}
	if got, err := m.GetTask(context.Background(), "running"); err != nil || got.State != StateCanceling {
		t.Fatalf("GetTask after active dismiss = %+v err=%v, want canceling", got, err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("dismiss did not cancel active task")
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, err := m.GetTask(context.Background(), "running")
		if errors.Is(err, ErrNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("active task was not removed after cancellation: %v", err)
		}
		time.Sleep(time.Millisecond)
	}

	m.Submit(context.Background(), Task{ID: "done", Type: TypeDeleteBatch}, func(context.Context, UpdateFunc) error {
		return nil
	})
	waitTaskState(t, m, "done", StateSucceeded)
	if err := m.DismissTask(context.Background(), "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetTask(context.Background(), "done"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTask after remove err=%v, want not found", err)
	}

	m.Submit(context.Background(), Task{
		ID:   "dismissible",
		Type: TypeDeleteBatch,
		Capabilities: Capabilities{
			Dismissible: true,
		},
	}, func(context.Context, UpdateFunc) error {
		return nil
	})
	waitTaskState(t, m, "dismissible", StateSucceeded)
	if err := m.DismissTask(context.Background(), "dismissible"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetTask(context.Background(), "dismissible"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTask after remove err=%v, want not found", err)
	}
}

func TestManagerDismissFinishedTasksRemovesTerminalTasks(t *testing.T) {
	m := NewManager()
	defer m.Close()
	m.Submit(context.Background(), Task{
		ID:   "done",
		Type: TypeDeleteBatch,
		Capabilities: Capabilities{
			Dismissible: true,
		},
	}, func(context.Context, UpdateFunc) error {
		return nil
	})
	waitTaskState(t, m, "done", StateSucceeded)

	m.Submit(context.Background(), Task{
		ID:   "kept-type",
		Type: TypeDownload,
		Capabilities: Capabilities{
			Dismissible: true,
		},
	}, func(context.Context, UpdateFunc) error {
		return nil
	})
	waitTaskState(t, m, "kept-type", StateSucceeded)

	m.Submit(context.Background(), Task{
		ID:   "kept-capability",
		Type: TypeDeleteBatch,
	}, func(context.Context, UpdateFunc) error {
		return nil
	})
	waitTaskState(t, m, "kept-capability", StateSucceeded)

	removed, err := m.DismissFinishedTasks(context.Background(), Filter{Types: []Type{TypeDeleteBatch}})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if _, err := m.GetTask(context.Background(), "done"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTask done err=%v, want not found", err)
	}
	if _, err := m.GetTask(context.Background(), "kept-type"); err != nil {
		t.Fatalf("kept-type missing: %v", err)
	}
	if _, err := m.GetTask(context.Background(), "kept-capability"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTask kept-capability err=%v, want not found", err)
	}
}

func TestManagerTaskEvents(t *testing.T) {
	m := NewManager()
	defer m.Close()
	sub := m.Subscribe(Filter{Types: []Type{TypeDeleteBatch}})
	defer sub.Close()

	m.Submit(context.Background(), Task{
		ID:   "delete-1",
		Type: TypeDeleteBatch,
		Capabilities: Capabilities{
			Dismissible: true,
		},
	}, func(context.Context, UpdateFunc) error {
		return nil
	})
	events := readTaskEvents(t, sub, 3)
	if events[0].Type != EventTaskUpdated || events[0].TaskID != "delete-1" || events[0].Task == nil {
		t.Fatalf("first event = %+v, want task update", events[0])
	}
	if events[len(events)-1].Task.State != StateSucceeded {
		t.Fatalf("last update = %+v, want succeeded", events[len(events)-1])
	}

	if err := m.DismissTask(context.Background(), "delete-1"); err != nil {
		t.Fatal(err)
	}
	events = readTaskEvents(t, sub, 1)
	if events[0].Type != EventTaskRemoved || events[0].TaskID != "delete-1" {
		t.Fatalf("remove event = %+v, want removed", events[0])
	}
}

func TestManagerDismissTaskDelegatesToSource(t *testing.T) {
	source := &dismissibleStaticSource{tasks: []Task{{
		ID:    "source-done",
		Type:  TypeUploadRemote,
		State: StateSucceeded,
		Capabilities: Capabilities{
			Dismissible: true,
		},
	}}}
	m := NewManager(source)
	defer m.Close()

	if err := m.DismissTask(context.Background(), "source-done"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetTask(context.Background(), "source-done"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTask after source remove err=%v, want not found", err)
	}
}

type staticSource struct {
	tasks []Task
}

func (s staticSource) ListTasks(context.Context, Filter) ([]Task, error) {
	return append([]Task(nil), s.tasks...), nil
}

func readTaskEvents(t *testing.T, sub *Subscription, minEvents int) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out []Event
	for len(out) < minEvents {
		events, err := sub.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, events...)
	}
	return out
}

func (s staticSource) GetTask(_ context.Context, id string) (Task, error) {
	for _, item := range s.tasks {
		if item.ID == id {
			return item, nil
		}
	}
	return Task{}, ErrNotFound
}

func (s staticSource) CancelTask(context.Context, string) error {
	return ErrNotFound
}

func (s staticSource) RetryTask(context.Context, string) error {
	return ErrNotFound
}

type dismissibleStaticSource struct {
	tasks []Task
}

func (s *dismissibleStaticSource) ListTasks(_ context.Context, _ Filter) ([]Task, error) {
	return append([]Task(nil), s.tasks...), nil
}

func (s *dismissibleStaticSource) GetTask(_ context.Context, id string) (Task, error) {
	for _, item := range s.tasks {
		if item.ID == id {
			return item, nil
		}
	}
	return Task{}, ErrNotFound
}

func (s *dismissibleStaticSource) CancelTask(context.Context, string) error {
	return ErrNotFound
}

func (s *dismissibleStaticSource) RetryTask(context.Context, string) error {
	return ErrNotFound
}

func (s *dismissibleStaticSource) DismissTask(_ context.Context, id string) error {
	for i, item := range s.tasks {
		if item.ID != id {
			continue
		}
		s.tasks = append(s.tasks[:i], s.tasks[i+1:]...)
		return nil
	}
	return ErrNotFound
}

func (s *dismissibleStaticSource) DismissFinishedTasks(_ context.Context, filter Filter) (int, error) {
	kept := s.tasks[:0]
	removed := 0
	for _, item := range s.tasks {
		if filter.Match(item) && isTerminalState(item.State) && item.Capabilities.Dismissible {
			removed++
			continue
		}
		kept = append(kept, item)
	}
	s.tasks = kept
	return removed, nil
}

func waitTaskState(t *testing.T, m *Manager, id string, want State) Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		item, err := m.GetTask(context.Background(), id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			t.Fatal(err)
		}
		if item.State == want {
			return item
		}
		if item.State == StateFailed {
			t.Fatalf("task failed: %+v", item)
		}
		time.Sleep(10 * time.Millisecond)
	}
	item, err := m.GetTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("task %s state=%s, want %s", id, item.State, want)
	return Task{}
}

// Submitting without a visibility declaration falls back to the type's
// declaration; an explicit declaration (any origin may be hidden) survives.
func TestSubmitNormalizesVisibilityFromTypeDeclaration(t *testing.T) {
	t.Parallel()
	m := NewManager()
	defer m.Close()
	undeclared := m.Submit(context.Background(), Task{ID: "from-type", Type: TypeUploadRemote, Scope: ScopeUser}, func(context.Context, UpdateFunc) error { return nil })
	if undeclared.Visibility != VisibilityForType(TypeUploadRemote) {
		t.Fatalf("submitted visibility = %q, want type declaration %q", undeclared.Visibility, VisibilityForType(TypeUploadRemote))
	}
	explicit := m.Submit(context.Background(), Task{ID: "hidden-user", Type: TypeUploadRemote, Scope: ScopeUser, Visibility: VisibilityHidden}, func(context.Context, UpdateFunc) error { return nil })
	if explicit.Visibility != VisibilityHidden {
		t.Fatalf("explicit visibility = %q, want hidden preserved", explicit.Visibility)
	}
}

// Visibility is independent of origin (glossary: 可见性): the app's default
// list filter matches on visibility alone, so any origin's tasks may be
// visible or hidden.
func TestFilterMatchByVisibility(t *testing.T) {
	cases := []struct {
		filter Filter
		task   Task
		want   bool
	}{
		{Filter{Visibility: VisibilityVisible}, Task{ID: "u-v", Scope: ScopeUser, Visibility: VisibilityVisible}, true},
		{Filter{Visibility: VisibilityVisible}, Task{ID: "i-v", Scope: ScopeInternal, Visibility: VisibilityVisible}, true},
		{Filter{Visibility: VisibilityVisible}, Task{ID: "s-v", Scope: ScopeSync, Visibility: VisibilityVisible}, true},
		{Filter{Visibility: VisibilityVisible}, Task{ID: "u-h", Scope: ScopeUser, Visibility: VisibilityHidden}, false},
		{Filter{Visibility: VisibilityVisible}, Task{ID: "i-h", Scope: ScopeInternal, Visibility: VisibilityHidden}, false},
		{Filter{Visibility: VisibilityHidden}, Task{ID: "i-h", Scope: ScopeInternal, Visibility: VisibilityHidden}, true},
		{Filter{Visibility: VisibilityHidden}, Task{ID: "i-v", Scope: ScopeInternal, Visibility: VisibilityVisible}, false},
		{Filter{}, Task{ID: "i-v", Scope: ScopeInternal, Visibility: VisibilityVisible}, true},
	}
	for _, tc := range cases {
		if got := tc.filter.Match(tc.task); got != tc.want {
			t.Fatalf("Filter%+v.Match(%s) = %v, want %v", tc.filter, tc.task.ID, got, tc.want)
		}
	}
}

func TestFilterMatchByScope(t *testing.T) {
	userTask := Task{ID: "user-1", Type: TypeMoveRemote, Scope: ScopeUser}
	syncTask := Task{ID: "sync-1", Type: TypeUploadRemote, Scope: ScopeSync}
	internalTask := Task{ID: "internal-1", Type: TypeUploadRemote, Scope: ScopeInternal}
	unspecified := Task{ID: "none-1", Type: TypeMoveRemote}
	cases := []struct {
		filter Filter
		task   Task
		want   bool
	}{
		{Filter{Scope: ScopeUser}, userTask, true},
		{Filter{Scope: ScopeUser}, syncTask, false},
		{Filter{Scope: ScopeUser}, internalTask, false},
		{Filter{Scope: ScopeUser}, unspecified, false},
		{Filter{Scope: ScopeSync}, userTask, false},
		{Filter{Scope: ScopeSync}, syncTask, true},
		{Filter{Scope: ScopeSync}, internalTask, false},
		{Filter{Scope: ScopeInternal}, userTask, false},
		{Filter{Scope: ScopeInternal}, syncTask, false},
		{Filter{Scope: ScopeInternal}, internalTask, true},
		{Filter{Types: []Type{TypeUploadRemote}}, syncTask, true},
	}
	for _, tc := range cases {
		if got := tc.filter.Match(tc.task); got != tc.want {
			t.Fatalf("Filter%+v.Match(%s/%s) = %v, want %v", tc.filter, tc.task.ID, tc.task.Scope, got, tc.want)
		}
	}
}
