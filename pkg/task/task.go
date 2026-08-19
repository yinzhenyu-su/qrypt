package task

import (
	"errors"
	"time"
)

var ErrNotFound = errors.New("task: not found")

type Type string

const (
	TypeUploadRemote        Type = "upload_remote"
	TypeUploadBatch         Type = "upload_batch"
	TypeUploadStreamBatch   Type = "upload_stream_batch"
	TypeUploadStreamDirect  Type = "upload_stream_direct"
	TypeDownload            Type = "download"
	TypeDownloadStreamBatch Type = "download_stream_batch"
	TypeDeleteRemote        Type = "delete_remote"
	TypeDeleteBatch         Type = "delete_batch"
	TypeCopy                Type = "copy"
	TypeMoveRemote          Type = "move_remote"
)

type State string

const (
	StateQueued    State = "queued"
	StateScheduled State = "scheduled"
	StateRunning   State = "running"
	StateRetryWait State = "retry_wait"
	StateSucceeded State = "succeeded"
	// StatePartialFailed means a batch task completed with at least one failed item.
	StatePartialFailed State = "partial_failed"
	StateFailed        State = "failed"
	StateCanceled      State = "canceled"
	StateWaitingInput  State = "waiting_input"
	StateWaitingOutput State = "waiting_output"
)

type Scope string

const (
	ScopeUser Scope = "user"
	ScopeSync Scope = "sync"
)

type Task struct {
	ID           string         `json:"id"`
	Type         Type           `json:"type"`
	State        State          `json:"state"`
	Scope        Scope          `json:"scope,omitempty"`
	Mount        string         `json:"mount,omitempty"`
	Path         string         `json:"path,omitempty"`
	Name         string         `json:"name,omitempty"`
	CreatedAt    time.Time      `json:"created_at,omitempty"`
	StartedAt    time.Time      `json:"started_at,omitempty"`
	UpdatedAt    time.Time      `json:"updated_at,omitempty"`
	CompletedAt  time.Time      `json:"completed_at,omitempty"`
	RetryCount   int            `json:"retry_count,omitempty"`
	NextAttempt  time.Time      `json:"next_attempt_at,omitempty"`
	Version      uint64         `json:"version,omitempty"`
	Progress     Progress       `json:"progress,omitempty"`
	Capabilities Capabilities   `json:"capabilities,omitempty"`
	Error        *Error         `json:"error,omitempty"`
	Result       Result         `json:"result,omitempty"`
	Detail       map[string]any `json:"detail,omitempty"`
}

type Progress struct {
	SourceBytesDone    int64  `json:"source_bytes_done,omitempty"`
	SourceBytesTotal   int64  `json:"source_bytes_total,omitempty"`
	CloudBytesDone     int64  `json:"cloud_bytes_done,omitempty"`
	CloudBytesTotal    int64  `json:"cloud_bytes_total,omitempty"`
	StagingBytesDone   int64  `json:"staging_bytes_done,omitempty"`
	StagingBytesTotal  int64  `json:"staging_bytes_total,omitempty"`
	OutputBytesDone    int64  `json:"output_bytes_done,omitempty"`
	OutputBytesTotal   int64  `json:"output_bytes_total,omitempty"`
	TransferBytesDone  int64  `json:"transfer_bytes_done,omitempty"`
	TransferBytesTotal int64  `json:"transfer_bytes_total,omitempty"`
	ItemsDone          int64  `json:"items_done,omitempty"`
	ItemsTotal         int64  `json:"items_total,omitempty"`
	ItemsFailed        int64  `json:"items_failed,omitempty"`
	CurrentPath        string `json:"current_path,omitempty"`
	Phase              string `json:"phase,omitempty"`
	SpeedBPS           int64  `json:"speed_bps,omitempty"`
	ETAMS              int64  `json:"eta_ms,omitempty"`
}

type Capabilities struct {
	Cancelable  bool `json:"cancelable"`
	Retryable   bool `json:"retryable"`
	Dismissible bool `json:"dismissible"`
	Persistent  bool `json:"persistent"`
}

type Error struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

type Result struct {
	Items []ItemResult `json:"items,omitempty"`
}

type EventType string

const (
	EventTaskUpdated EventType = "task_updated"
	EventTaskRemoved EventType = "task_removed"
)

type Event struct {
	Seq    uint64    `json:"seq"`
	Type   EventType `json:"type"`
	TaskID string    `json:"task_id"`
	Task   *Task     `json:"task,omitempty"`
}

type ItemResult struct {
	Path               string           `json:"path,omitempty"`
	ItemID             string           `json:"item_id,omitempty"`
	SourcePath         string           `json:"source_path,omitempty"`
	DestPath           string           `json:"dest_path,omitempty"`
	Mount              string           `json:"mount,omitempty"`
	State              State            `json:"state"`
	Phase              string           `json:"phase,omitempty"`
	Error              *Error           `json:"error,omitempty"`
	RemoteID           string           `json:"remote_id,omitempty"`
	SourceBytesDone    int64            `json:"source_bytes_done,omitempty"`
	SourceBytesTotal   int64            `json:"source_bytes_total,omitempty"`
	CloudBytesDone     int64            `json:"cloud_bytes_done,omitempty"`
	CloudBytesTotal    int64            `json:"cloud_bytes_total,omitempty"`
	StagingBytesDone   int64            `json:"staging_bytes_done,omitempty"`
	StagingBytesTotal  int64            `json:"staging_bytes_total,omitempty"`
	OutputBytesDone    int64            `json:"output_bytes_done,omitempty"`
	OutputBytesTotal   int64            `json:"output_bytes_total,omitempty"`
	TransferBytesDone  int64            `json:"transfer_bytes_done,omitempty"`
	TransferBytesTotal int64            `json:"transfer_bytes_total,omitempty"`
	ResumeOffset       int64            `json:"resume_offset,omitempty"`
	Capabilities       ItemCapabilities `json:"capabilities,omitempty"`
}

type ItemCapabilities struct {
	OpenInput   bool `json:"open_input,omitempty"`
	CommitInput bool `json:"commit_input,omitempty"`
	OpenOutput  bool `json:"open_output,omitempty"`
	Cancelable  bool `json:"cancelable,omitempty"`
}

type Request struct {
	Type    Type           `json:"type"`
	Scope   Scope          `json:"scope,omitempty"`
	Items   []Item         `json:"items,omitempty"`
	Options Options        `json:"options,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

type Item struct {
	Path         string `json:"path,omitempty"`
	Name         string `json:"name,omitempty"`
	Mount        string `json:"mount,omitempty"`
	SourcePath   string `json:"source_path,omitempty"`
	DestPath     string `json:"dest_path,omitempty"`
	ItemID       string `json:"item_id,omitempty"`
	RelativePath string `json:"relative_path,omitempty"`
	Size         int64  `json:"size,omitempty"`
}

type Options struct {
	Overwrite             bool   `json:"overwrite,omitempty"`
	Recursive             bool   `json:"recursive,omitempty"`
	Verify                bool   `json:"verify,omitempty"`
	DeleteSourceAfterCopy bool   `json:"delete_source_after_copy,omitempty"`
	ConflictPolicy        string `json:"conflict_policy,omitempty"`
	Concurrency           int    `json:"concurrency,omitempty"`
}

type Filter struct {
	ID     string  `json:"id,omitempty"`
	Types  []Type  `json:"types,omitempty"`
	States []State `json:"states,omitempty"`
	Scope  Scope   `json:"scope,omitempty"`
	Mount  string  `json:"mount,omitempty"`
	Path   string  `json:"path,omitempty"`
	Limit  int     `json:"limit,omitempty"`
}

type ItemFilter struct {
	ItemID string  `json:"item_id,omitempty"`
	States []State `json:"states,omitempty"`
	Limit  int     `json:"limit,omitempty"`
}

func (f Filter) Match(t Task) bool {
	if f.ID != "" && t.ID != f.ID {
		return false
	}
	if f.Scope != "" && t.Scope != f.Scope {
		return false
	}
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

func (f ItemFilter) Match(item ItemResult) bool {
	if f.ItemID != "" && item.ItemID != f.ItemID {
		return false
	}
	if len(f.States) > 0 {
		ok := false
		for _, state := range f.States {
			if item.State == state {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
