package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type MoveTaskRequest struct {
	SourcePath string `json:"source_path"`
	DestPath   string `json:"dest_path"`
	Overwrite  bool   `json:"overwrite,omitempty"`
	Recursive  bool   `json:"recursive,omitempty"`
}

type taskRecord struct {
	task task.Task
}

func (c *Core) CreateMoveTask(ctx context.Context, req MoveTaskRequest) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	if err := ctx.Err(); err != nil {
		return task.Task{}, err
	}
	req.SourcePath = vfs.CleanVirtualPath(req.SourcePath)
	req.DestPath = vfs.CleanVirtualPath(req.DestPath)
	if req.SourcePath == "/" || req.DestPath == "/" {
		return task.Task{}, fmt.Errorf("core: move source and destination must be files or directories")
	}
	if req.SourcePath == req.DestPath {
		return task.Task{}, fmt.Errorf("core: move source and destination are the same")
	}

	now := timeutil.Now()
	item := task.Task{
		ID:        newMoveTaskID(),
		Type:      task.TypeMoveRemote,
		State:     task.StateRunning,
		Scope:     task.ScopeUser,
		Path:      req.SourcePath,
		Name:      path.Base(req.SourcePath),
		CreatedAt: now,
		StartedAt: now,
		UpdatedAt: now,
		Detail: map[string]any{
			"source_path": req.SourcePath,
			"dest_path":   req.DestPath,
			"overwrite":   req.Overwrite,
			"recursive":   req.Recursive,
		},
	}

	sourceMount, destMount, crossQryptMount := moveMounts(req.SourcePath, req.DestPath, c.fs)
	if sourceMount != "" {
		item.Mount = sourceMount
		item.Detail["source_mount"] = sourceMount
	}
	if destMount != "" {
		item.Detail["dest_mount"] = destMount
	}

	var err error
	if crossQryptMount {
		item.Detail["mode"] = "copy_delete"
		err = c.runCrossMountMove(ctx, req, &item)
	} else {
		item.Detail["mode"] = "server_move"
		err = c.fs.Rename(ctx, req.SourcePath, req.DestPath)
	}
	c.finishMoveTask(&item, err)
	c.recordMoveTask(item)
	return item, nil
}

func (c *Core) runCrossMountMove(ctx context.Context, req MoveTaskRequest, item *task.Task) error {
	entry, err := c.fs.Stat(ctx, req.SourcePath)
	if err != nil {
		item.Detail["phase"] = "stat_source"
		return err
	}
	if entry.IsDir && !req.Recursive {
		item.Detail["phase"] = "validate_source"
		return fmt.Errorf("core: source %q is a directory; recursive move is required", req.SourcePath)
	}
	source, ok := c.fs.(control.DriverCopySource)
	if !ok {
		item.Detail["phase"] = "validate_source"
		return fmt.Errorf("core: cross-mount move requires driver copy support")
	}
	if entry.IsDir {
		if path.Base(req.SourcePath) != path.Base(req.DestPath) {
			item.Detail["phase"] = "validate_dest"
			return fmt.Errorf("core: recursive cross-mount move currently requires destination name %q", path.Base(req.SourcePath))
		}
		result := control.RunDirectDriverCopyDir(ctx, c.fs, source, req.SourcePath, path.Dir(req.DestPath), req.Overwrite)
		item.Detail["copy_op_id"] = result.OpID
		item.BytesTotal = result.Bytes
		item.BytesDone = result.Bytes
		if !result.Pass {
			item.Detail["phase"] = "copy"
			if result.Error != "" {
				return fmt.Errorf("%s", result.Error)
			}
			return fmt.Errorf("core: directory copy failed")
		}
	} else {
		result := control.RunDirectDriverCopy(ctx, source, req.SourcePath, req.DestPath, req.Overwrite)
		item.Detail["copy_op_id"] = result.OpID
		item.BytesTotal = result.Bytes
		item.BytesDone = result.Bytes
		if !result.Pass {
			item.Detail["phase"] = "copy"
			return fmt.Errorf("%s", control.DriverCopyError(result))
		}
	}

	item.Detail["phase"] = "delete_source"
	return removeMoveSource(ctx, source, req.SourcePath, entry.IsDir)
}

func removeMoveSource(ctx context.Context, source control.DriverCopySource, sourcePath string, isDir bool) error {
	info, err := source.DebugResolve(ctx, sourcePath, false)
	if err != nil {
		return err
	}
	drivers := map[string]drive.Driver{}
	for _, named := range source.Drivers() {
		drivers[named.Name] = named.Driver
	}
	driver := drivers[info.Mount]
	if driver == nil && len(drivers) == 1 {
		for _, item := range drivers {
			driver = item
		}
	}
	if driver == nil {
		return fmt.Errorf("core: source driver %q not found", info.Mount)
	}
	return driver.Remove(ctx, drive.Entry{
		ID:       info.RemoteID,
		ParentID: info.ParentID,
		Name:     info.PlainName,
		IsDir:    isDir,
		Size:     info.Size,
	})
}

func (c *Core) finishMoveTask(item *task.Task, err error) {
	now := timeutil.Now()
	item.UpdatedAt = now
	item.CompletedAt = now
	item.Cancelable = false
	item.Retryable = false
	if err != nil {
		item.State = task.StateFailed
		item.LastError = err.Error()
		if _, ok := item.Detail["phase"]; !ok {
			item.Detail["phase"] = "move"
		}
		return
	}
	item.State = task.StateSucceeded
	item.Detail["phase"] = "complete"
}

func (c *Core) recordMoveTask(item task.Task) {
	c.taskMu.Lock()
	defer c.taskMu.Unlock()
	if c.moveTasks == nil {
		c.moveTasks = map[string]taskRecord{}
	}
	c.moveTasks[item.ID] = taskRecord{task: item}
}

func (c *Core) moveTaskSnapshot(filter task.Filter) []task.Task {
	c.taskMu.Lock()
	defer c.taskMu.Unlock()
	if len(c.moveTasks) == 0 {
		return nil
	}
	tasks := make([]task.Task, 0, len(c.moveTasks))
	for _, record := range c.moveTasks {
		if filter.Match(record.task) {
			tasks = append(tasks, record.task)
		}
	}
	return tasks
}

func (c *Core) hasMoveTask(id string) bool {
	c.taskMu.Lock()
	defer c.taskMu.Unlock()
	_, ok := c.moveTasks[id]
	return ok
}

func moveMounts(sourcePath, destPath string, fs vfs.FileSystem) (string, string, bool) {
	if _, ok := fs.(*vfs.Namespace); !ok {
		return "", "", false
	}
	sourceMount, _, sourceRoot := splitMoveNamespacePath(sourcePath)
	destMount, _, destRoot := splitMoveNamespacePath(destPath)
	if sourceRoot || destRoot {
		return sourceMount, destMount, false
	}
	return sourceMount, destMount, sourceMount != "" && destMount != "" && sourceMount != destMount
}

func splitMoveNamespacePath(p string) (string, string, bool) {
	p = vfs.CleanVirtualPath(p)
	if p == "/" {
		return "", "/", true
	}
	trimmed := strings.TrimPrefix(p, "/")
	mount, rest, ok := strings.Cut(trimmed, "/")
	if !ok {
		return mount, "/", false
	}
	return mount, "/" + rest, false
}

func newMoveTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "move-" + strconv.FormatInt(timeutil.Now().UnixNano(), 36)
	}
	return "move-" + hex.EncodeToString(b[:])
}
