package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// The share request and response types are a provider-neutral application
// contract owned by pkg/share. Core and the local transport share one
// definition without Core depending on the transport package.
type (
	ShareLinkCreateRequest = shareapi.LinkCreateRequest
	ShareLinkListRequest   = shareapi.LinkListRequest
	ShareLinkUpdateRequest = shareapi.LinkUpdateRequest
)

// ShareControlService exposes the provider-neutral share surface over the local
// control socket. The returned service classifies request-shape errors so the
// transport can pick an HTTP status without parsing provider text.
func (c *Core) ShareControlService() shareapi.Service {
	return shareControlService{core: c}
}

type shareControlService struct {
	core *Core
}

var _ shareapi.Service = shareControlService{}

func (s shareControlService) CreateShareLink(ctx context.Context, req shareapi.LinkCreateRequest) (drive.Share, error) {
	created, err := s.core.CreateShareLink(ctx, req)
	return created, classifyShareControlError(err)
}

func (s shareControlService) ListShareLinks(ctx context.Context, req shareapi.LinkListRequest) (drive.SharePage, error) {
	page, err := s.core.ListShareLinks(ctx, req)
	return page, classifyShareControlError(err)
}

func (s shareControlService) GetShareLink(ctx context.Context, mount, id string) (drive.Share, error) {
	share, err := s.core.GetShareLink(ctx, mount, id)
	return share, classifyShareControlError(err)
}

func (s shareControlService) UpdateShareLink(ctx context.Context, req shareapi.LinkUpdateRequest) (drive.Share, error) {
	updated, err := s.core.UpdateShareLink(ctx, req)
	return updated, classifyShareControlError(err)
}

func (s shareControlService) RemoveShareLink(ctx context.Context, mount, id string) error {
	return classifyShareControlError(s.core.RemoveShareLink(ctx, mount, id))
}

func (s shareControlService) InspectShareLink(ctx context.Context, req shareapi.InspectRequest) (shareapi.InspectResult, error) {
	preview, err := s.core.InspectShareLink(ctx, req)
	return preview, classifyShareControlError(err)
}

// CreateShareTask creates a receive task from a provider-neutral operation.
// The download destination, not the caller's operation field, selects fetch or
// save.
func (s shareControlService) CreateShareTask(ctx context.Context, req task.OperationRequest) (task.Task, error) {
	if s.core == nil || s.core.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	if req.Share == nil {
		return task.Task{}, classifyShareControlError(fmt.Errorf("%w: share inputs are required", task.ErrInvalidOperation))
	}
	if strings.TrimSpace(req.Share.DownloadTo) != "" {
		req.Operation = task.OperationDownload
	} else {
		req.Operation = task.OperationShare
	}
	created, err := s.core.CreateOperation(ctx, req)
	return created, classifyShareControlError(err)
}

// ListShareTasks lists receive tasks. The control server owns the default
// active-plus-failed view; this method only adds the share types when the
// caller did not narrow them.
func (s shareControlService) ListShareTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	if len(filter.Types) == 0 {
		filter.Types = []task.Type{task.TypeShareSave, task.TypeShareFetch}
	}
	listed, err := s.core.ListTasks(ctx, filter)
	return listed, classifyShareControlError(err)
}

// GetShareTask resolves one receive task by its qrypt id.
func (s shareControlService) GetShareTask(ctx context.Context, id string) (task.Task, error) {
	if id == "" {
		return task.Task{}, classifyShareControlError(fmt.Errorf("%w: task id required", task.ErrNotFound))
	}
	current, err := s.core.GetTask(ctx, id)
	if err != nil {
		return task.Task{}, classifyShareControlError(err)
	}
	switch current.Type {
	case task.TypeShareSave, task.TypeShareFetch:
		return current, nil
	default:
		return task.Task{}, classifyShareControlError(fmt.Errorf("%w: task %q is not a receive task", task.ErrNotFound, id))
	}
}

// WaitShareTask observes an existing receive task to a terminal state without
// mutating it. A zero timeout waits indefinitely.
func (s shareControlService) WaitShareTask(ctx context.Context, id string, timeout time.Duration) (task.Task, error) {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	interval := s.core.offlinePollEvery()
	if interval <= 0 {
		interval = defaultOfflinePollInterval
	}
	for {
		if err := ctx.Err(); err != nil {
			return task.Task{}, err
		}
		current, err := s.GetShareTask(ctx, id)
		if err != nil {
			return task.Task{}, err
		}
		if offlineTaskTerminal(current.State) {
			return current, nil
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return current, drive.NewShareError(drive.ShareErrTimeout, "", fmt.Sprintf("receive task %s did not finish within %s", id, timeout))
		}
		wait := interval
		if !deadline.IsZero() {
			if remaining := time.Until(deadline); remaining < wait {
				wait = remaining
			}
		}
		if err := sleepContext(ctx, wait); err != nil {
			return current, err
		}
	}
}

func (s shareControlService) CancelTask(ctx context.Context, id string) error {
	return classifyShareControlError(s.core.CancelTask(ctx, id))
}

func (s shareControlService) RetryTask(ctx context.Context, id string) error {
	return classifyShareControlError(s.core.RetryTask(ctx, id))
}

// classifyShareControlError tags request-shape failures as usage so the control
// transport can return 400 (CLI exit 2), missing tasks and links as 404, and
// leaves every other failure as a runtime error.
func classifyShareControlError(err error) error {
	if err == nil {
		return nil
	}
	if provider, ok := drive.AsShareError(err); ok {
		// A provider-neutral share error already carries its own class.
		return provider
	}
	if _, ok := err.(*shareapi.RequestError); ok {
		return err
	}
	switch {
	case errors.Is(err, ErrSharePathsRequired), errors.Is(err, ErrSharePathNotFound),
		errors.Is(err, ErrShareRootPath), errors.Is(err, ErrShareCrossMount),
		errors.Is(err, ErrShareMountRequired), errors.Is(err, ErrShareMountNotFound),
		errors.Is(err, ErrShareMountConflict), errors.Is(err, ErrShareUnsupported),
		errors.Is(err, ErrShareEncryptedDestination), errors.Is(err, ErrShareDestinationMissing),
		errors.Is(err, ErrShareLinkInvalid), errors.Is(err, ErrShareLinkProviderMismatch),
		errors.Is(err, ErrShareCrossProviderUnsupported), errors.Is(err, task.ErrInvalidOperation):
		return &shareapi.RequestError{Class: shareapi.ErrorUsage, Message: err.Error(), Err: err}
	case errors.Is(err, task.ErrNotFound):
		return &shareapi.RequestError{Class: shareapi.ErrorNotFound, Message: err.Error(), Err: err}
	}
	return err
}
