package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// Core implements control.OfflineService, the provider-neutral surface the
// local control server exposes. No method requires a provider name, driver
// name, socket path, or provider task identifier.
var _ control.OfflineService = (*Core)(nil)

// CreateOfflineTask creates an offline task from a provider-neutral request.
// The operation is forced to download: offline work never needs a new
// operation kind.
func (c *Core) CreateOfflineTask(ctx context.Context, req task.OperationRequest) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	req.Operation = task.OperationDownload
	if req.Offline == nil {
		return task.Task{}, classifyOfflineRequestError(fmt.Errorf("%w: offline inputs are required", task.ErrInvalidOperation))
	}
	created, err := c.CreateOperation(ctx, req)
	return created, classifyOfflineRequestError(err)
}

// classifyOfflineRequestError tags request-shape failures as usage so the
// control transport can return 400 (CLI exit 2) and missing tasks as 404.
// Everything else stays a runtime failure.
func classifyOfflineRequestError(err error) error {
	if err == nil {
		return nil
	}
	var input *OfflineInputError
	if errors.As(err, &input) {
		return &control.OfflineError{Class: control.OfflineErrorUsage, Code: input.Code, Message: input.Message, Err: err}
	}
	switch {
	case errors.Is(err, ErrOfflineMountRequired), errors.Is(err, ErrOfflineMountConflict),
		errors.Is(err, ErrOfflineMountNotFound), errors.Is(err, ErrOfflineEncryptedParseDestination),
		errors.Is(err, ErrOfflineParseDestinationMissing), errors.Is(err, ErrOfflineSameDestination),
		errors.Is(err, ErrOfflineSourceUnsupported), errors.Is(err, task.ErrInvalidOperation):
		return &control.OfflineError{Class: control.OfflineErrorUsage, Message: err.Error(), Err: err}
	case errors.Is(err, task.ErrNotFound):
		return &control.OfflineError{Class: control.OfflineErrorNotFound, Message: err.Error(), Err: err}
	}
	return err
}

// ListOfflineTasks lists offline tasks under the given filter. The control
// server owns the default active-plus-failed view; this method only adds the
// offline types when the caller did not narrow them.
func (c *Core) ListOfflineTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	if len(filter.Types) == 0 {
		filter.Types = []task.Type{task.TypeOfflineDownload, task.TypeOfflineFetch}
	}
	return c.ListTasks(ctx, filter)
}

// GetOfflineTask resolves one offline task by its qrypt id.
func (c *Core) GetOfflineTask(ctx context.Context, id string) (task.Task, error) {
	if id == "" {
		return task.Task{}, fmt.Errorf("core: task id required")
	}
	current, err := c.GetTask(ctx, id)
	if err != nil {
		return task.Task{}, classifyOfflineRequestError(err)
	}
	switch current.Type {
	case task.TypeOfflineDownload, task.TypeOfflineFetch:
		return current, nil
	default:
		return task.Task{}, classifyOfflineRequestError(fmt.Errorf("%w: task %q is not an offline task", task.ErrNotFound, id))
	}
}
