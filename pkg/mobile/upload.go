package mobile

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

const uploadStreamCopyBufferSize = 256 * 1024

type uploadFinishResult struct {
	Entry entry     `json:"entry"`
	Task  task.Task `json:"task"`
}

// UploadLocalFileJSON uploads a stable local filesystem path by streaming it
// into a qrypt upload stream task. The call returns once the file has been
// staged and the cloud upload is queued; it does not wait for remote upload
// completion. The returned task is user-visible: it appears in the default
// mobile task list and emits task_updated events for staging/cloud progress.
func UploadLocalFileJSON(coreID, localPath, remotePath string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	if info.IsDir() {
		return resultJSON(nil, wrapError(fmt.Errorf("mobile: %s is a directory", localPath)))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	resolvedRemotePath, err := withCore(s, func(c *core.Core) (string, error) {
		return c.ResolveUploadDestination(remotePath, filepath.Base(localPath))
	})
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	created, err := withCore(s, func(c *core.Core) (task.Task, error) {
		return c.CreateTask(ctx, task.Request{
			Type: task.TypeUploadStreamBatch,
			Items: []task.Item{{
				ItemID:   "local-1",
				DestPath: resolvedRemotePath,
				Name:     filepath.Base(localPath),
				Size:     info.Size(),
			}},
		})
	})
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	itemID, err := firstUploadStreamItemID(created)
	if err != nil {
		cancelCreatedTask(ctx, s, created.ID)
		return resultJSON(nil, wrapError(err))
	}
	handleID, err := openTaskUploadItem(coreID, created.ID, itemID, deadlineMS)
	if err != nil {
		cancelCreatedTask(ctx, s, created.ID)
		return resultJSON(nil, wrapError(err))
	}
	file, err := os.Open(localPath)
	if err != nil {
		_ = failUploadItem(handleID, "local_io", err.Error())
		return resultJSON(nil, wrapError(err))
	}
	buf := make([]byte, uploadStreamCopyBufferSize)
	streamErr := func() error {
		defer file.Close()
		for {
			n, readErr := file.Read(buf)
			if n > 0 {
				written, writeErr := writeUploadItem(handleID, buf[:n], deadlineMS)
				if writeErr != nil {
					return writeErr
				}
				if written != n {
					return fmt.Errorf("mobile: short staging write: wrote %d of %d", written, n)
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}()
	if streamErr != nil {
		_ = failUploadItem(handleID, "local_io", streamErr.Error())
		return resultJSON(nil, wrapError(streamErr))
	}
	if err := commitUploadItem(handleID, deadlineMS); err != nil {
		// The staged upload never became a real upload; terminate the task so
		// it does not linger in queued state without a usable handle.
		cancelCreatedTask(ctx, s, created.ID)
		return resultJSON(nil, wrapError(err))
	}
	entryItem, err := withCore(s, func(c *core.Core) (drive.Entry, error) { return c.Stat(ctx, resolvedRemotePath) })
	if err != nil {
		entryItem = drive.Entry{Name: filepath.Base(localPath), Size: info.Size()}
	}
	return resultJSON(uploadFinishResult{Entry: fromDriveEntry(entryItem, resolvedRemotePath), Task: created}, nil)
}

func firstUploadStreamItemID(item task.Task) (string, error) {
	if len(item.Result.Items) == 0 {
		return "", fmt.Errorf("mobile: upload task %s has no items", item.ID)
	}
	id := item.Result.Items[0].ItemID
	if id == "" {
		id = "local-1"
	}
	return id, nil
}

// cancelCreatedTask best-effort terminates a task created by this upload
// flow when a later step fails, so the task does not linger in queued state.
func cancelCreatedTask(ctx context.Context, s *session, taskID string) {
	if taskID == "" {
		return
	}
	if err := withCoreErr(s, func(c *core.Core) error { return c.CancelTask(ctx, taskID) }); err != nil {
		logging.L.Warnf("[MOBILE] upload: cancel dangling task %s: %v", taskID, err)
	}
}
