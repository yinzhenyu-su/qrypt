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
	if _, err := m.GetTask(context.Background(), "running"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTask after active dismiss err=%v, want not found", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("dismiss did not cancel active task")
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

func readTaskEvents(t *testing.T, sub *Subscription, min int) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out []Event
	for len(out) < min {
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
