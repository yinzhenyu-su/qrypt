package task

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type PersistentStore struct {
	mu             sync.Mutex
	path           string
	inner          *MemoryStore
	persistenceMu  sync.RWMutex
	persistenceErr error
}

var ErrPersistence = errors.New("task: persistence failed")

type taskJournalEntry struct {
	Op   string `json:"op"`
	Task Task   `json:"task,omitempty"`
	ID   string `json:"id,omitempty"`
}

func NewPersistentStore(path string) (*PersistentStore, error) {
	if path == "" {
		return nil, fmt.Errorf("task: persistent store path required")
	}
	store := &PersistentStore{
		path:  path,
		inner: NewMemoryStore(),
	}
	if err := store.replay(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *PersistentStore) PutManaged(managed ManagedTask) Task {
	item := s.inner.PutManaged(managed)
	s.persistTask(item)
	return item
}

func (s *PersistentStore) GetManaged(id string) (ManagedTask, bool) {
	return s.inner.GetManaged(id)
}

func (s *PersistentStore) UpdateManaged(id string, fn func(*ManagedTask)) (ManagedTask, bool) {
	var changed bool
	managed, ok := s.inner.UpdateManaged(id, func(mt *ManagedTask) {
		before := taskDurableKey(mt.Task)
		fn(mt)
		changed = taskDurableKey(mt.Task) != before
		// 在 store 锁内先写 journal、后提交内存:重开/replay 时不会读到
		// 早于最新状态的记录(消除任务终态的内存可见性与持久化之间的窗口)。
		if changed && mt.Task.Capabilities.Persistent {
			s.recordPersistenceError(s.append(taskJournalEntry{Op: "update", ID: id, Task: mt.Task}))
		}
	})
	return managed, ok
}

func (s *PersistentStore) UpdateManagedInPlace(id string, fn func(*ManagedTask)) bool {
	var changed bool
	if !s.inner.UpdateManagedInPlace(id, func(mt *ManagedTask) {
		before := taskDurableKey(mt.Task)
		fn(mt)
		changed = taskDurableKey(mt.Task) != before
		// 与 UpdateManaged 一致:store 锁内先写 journal、后提交内存,
		// 消除重开/replay 读到滞后状态的窗口。
		if changed && mt.Task.Capabilities.Persistent {
			s.recordPersistenceError(s.append(taskJournalEntry{Op: "update", ID: id, Task: mt.Task}))
		}
	}) {
		return false
	}
	return true
}

// taskDurableKey summarizes the task fields that matter for crash recovery.
// Comparing these (instead of the whole task) lets the persistent store skip
// journal writes for pure progress updates.
func taskDurableKey(t Task) durableKey {
	var code, message string
	if t.Error != nil {
		code, message = t.Error.Code, t.Error.Message
	}
	return durableKey{
		state:        t.State,
		cancelable:   t.Capabilities.Cancelable,
		retryable:    t.Capabilities.Retryable,
		dismissible:  t.Capabilities.Dismissible,
		persistent:   t.Capabilities.Persistent,
		errorCode:    code,
		errorMessage: message,
	}
}

type durableKey struct {
	state        State
	cancelable   bool
	retryable    bool
	dismissible  bool
	persistent   bool
	errorCode    string
	errorMessage string
}

func (s *PersistentStore) DismissManaged(id string) bool {
	if !s.inner.DismissManaged(id) {
		return false
	}
	s.recordPersistenceError(s.append(taskJournalEntry{Op: "dismiss", ID: id}))
	return true
}

func (s *PersistentStore) ListManaged(filter Filter) []ManagedTask {
	return s.inner.ListManaged(filter)
}

func (s *PersistentStore) ForEachManaged(fn func(ManagedTask)) {
	s.inner.ForEachManaged(fn)
}

func (s *PersistentStore) replay() error {
	file, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry taskJournalEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return fmt.Errorf("task: replay %s: %w", s.path, err)
		}
		switch entry.Op {
		case "put", "update":
			if entry.Task.ID != "" && entry.Task.Capabilities.Persistent {
				s.inner.LoadTask(normalizeReplayedTask(entry.Task))
			}
		case "dismiss":
			if entry.ID != "" {
				s.inner.DismissManaged(entry.ID)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("task: replay %s: %w", s.path, err)
	}
	return nil
}

func normalizeReplayedTask(item Task) Task {
	switch item.State {
	case StateQueued, StateScheduled, StateRunning, StateRetryWait, StateWaitingInput, StateWaitingOutput:
		item.State = StateFailed
		item.Capabilities.Cancelable = false
		item.Capabilities.Retryable = false
		item.Capabilities.Dismissible = true
		item.Error = &Error{
			Code:    "interrupted",
			Message: "task was interrupted before completion",
		}
	case StateSucceeded, StatePartialFailed, StateFailed, StateCanceled:
		item.Capabilities.Cancelable = false
		item.Capabilities.Retryable = false
		item.Capabilities.Dismissible = true
		if item.Error != nil {
			item.Error.Retryable = false
		}
	}
	return item
}

func (s *PersistentStore) persistTask(item Task) {
	if !item.Capabilities.Persistent {
		return
	}
	s.recordPersistenceError(s.append(taskJournalEntry{Op: "update", ID: item.ID, Task: item}))
}

// PersistenceError returns the latest journal write error. Once degraded, a
// store remains degraded for its lifetime because a later successful append
// cannot reconstruct a record that was already lost.
func (s *PersistentStore) PersistenceError() error {
	if s == nil {
		return nil
	}
	s.persistenceMu.RLock()
	defer s.persistenceMu.RUnlock()
	return s.persistenceErr
}

func (s *PersistentStore) recordPersistenceError(err error) {
	if err == nil {
		return
	}
	s.persistenceMu.Lock()
	s.persistenceErr = fmt.Errorf("%w: %w", ErrPersistence, err)
	s.persistenceMu.Unlock()
}

func (s *PersistentStore) append(entry taskJournalEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if _, err := io.WriteString(file, "\n"); err != nil {
		return err
	}
	return nil
}
