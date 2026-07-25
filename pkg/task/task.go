package task

import "time"

type Type string

const (
	TypeUploadRemote Type = "upload_remote"
	TypeDownload     Type = "download"
	TypeDeleteRemote Type = "delete_remote"
	TypeCopy         Type = "copy"
	TypeMoveRemote   Type = "move_remote"
)

type State string

const (
	StateQueued    State = "queued"
	StateScheduled State = "scheduled"
	StateRunning   State = "running"
	StateRetryWait State = "retry_wait"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCanceled  State = "canceled"
)

type Scope string

const (
	ScopeUser      Scope = "user"
	ScopeWriteback Scope = "writeback"
)

type Task struct {
	ID          string         `json:"id"`
	Type        Type           `json:"type"`
	State       State          `json:"state"`
	Scope       Scope          `json:"scope,omitempty"`
	Mount       string         `json:"mount,omitempty"`
	Path        string         `json:"path,omitempty"`
	Name        string         `json:"name,omitempty"`
	BytesTotal  int64          `json:"bytes_total,omitempty"`
	BytesDone   int64          `json:"bytes_done,omitempty"`
	CreatedAt   time.Time      `json:"created_at,omitempty"`
	StartedAt   time.Time      `json:"started_at,omitempty"`
	UpdatedAt   time.Time      `json:"updated_at,omitempty"`
	CompletedAt time.Time      `json:"completed_at,omitempty"`
	RetryCount  int            `json:"retry_count,omitempty"`
	LastError   string         `json:"last_error,omitempty"`
	NextAttempt time.Time      `json:"next_attempt_at,omitempty"`
	Cancelable  bool           `json:"cancelable"`
	Retryable   bool           `json:"retryable"`
	Detail      map[string]any `json:"detail,omitempty"`
}

type Filter struct {
	Types  []Type  `json:"types,omitempty"`
	States []State `json:"states,omitempty"`
	Mount  string  `json:"mount,omitempty"`
	Path   string  `json:"path,omitempty"`
	Limit  int     `json:"limit,omitempty"`
}

func (f Filter) Match(t Task) bool {
	if len(f.Types) > 0 {
		ok := false
		for _, typ := range f.Types {
			if t.Type == typ {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.States) > 0 {
		ok := false
		for _, state := range f.States {
			if t.State == state {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if f.Mount != "" && t.Mount != f.Mount {
		return false
	}
	if f.Path != "" && t.Path != f.Path {
		return false
	}
	return true
}
