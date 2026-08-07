package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/yinzhenyu/qrypt/internal/drivecopy"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type moveTaskSpec struct {
	SourcePath  string `json:"source_path"`
	DestPath    string `json:"dest_path"`
	Overwrite   bool   `json:"overwrite,omitempty"`
	Recursive   bool   `json:"recursive,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
}

func (c *Core) createMoveTask(ctx context.Context, req moveTaskSpec) (task.Task, error) {
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
		State:     task.StateQueued,
		Scope:     task.ScopeUser,
		Path:      req.SourcePath,
		Name:      path.Base(req.SourcePath),
		CreatedAt: now,
		UpdatedAt: now,
		Capabilities: task.Capabilities{
			Cancelable: true,
		},
		Detail: map[string]any{
			"source_path": req.SourcePath,
			"dest_path":   req.DestPath,
			"overwrite":   req.Overwrite,
			"recursive":   req.Recursive,
			"concurrency": taskConcurrency(req.Concurrency),
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
	var crossCopySpec copyTaskSpec
	if crossQryptMount {
		crossCopySpec = copyTaskSpec{
			Items:                 []task.Item{{SourcePath: req.SourcePath, DestPath: req.DestPath}},
			OriginalItems:         []task.Item{{SourcePath: req.SourcePath, DestPath: req.DestPath}},
			Overwrite:             req.Overwrite,
			Recursive:             req.Recursive,
			Concurrency:           taskConcurrency(req.Concurrency),
			DeleteSourceAfterCopy: true,
		}
		var err error
		crossCopySpec, err = c.expandCopyTask(ctx, crossCopySpec)
		if err != nil {
			return task.Task{}, err
		}
		item.Progress.ItemsTotal = int64(len(crossCopySpec.Items))
		item.Detail["items"] = copyTaskDetailItems(crossCopySpec.Items)
		item.Capabilities.Persistent = true
		item.Capabilities.Dismissible = true
	}

	manager, err := c.taskManager()
	if err != nil {
		return task.Task{}, err
	}
	return manager.Submit(ctx, item, func(runCtx context.Context, update task.UpdateFunc) error {
		runItem := item
		runItem.Detail = cloneTaskDetail(item.Detail)
		var err error
		if crossQryptMount {
			update(func(item *task.Task) {
				item.Detail["mode"] = "copy_delete"
				item.Detail["phase"] = "copy"
			})
			return c.runCopyTask(runCtx, update, crossCopySpec)
		} else {
			update(func(item *task.Task) {
				item.Detail["mode"] = "server_move"
				item.Detail["phase"] = "move"
			})
			runItem.Detail["mode"] = "server_move"
			runItem.Detail["phase"] = "move"
			err = c.fs.Rename(runCtx, req.SourcePath, req.DestPath)
		}
		update(func(item *task.Task) {
			item.Progress.TransferBytesTotal = runItem.Progress.TransferBytesTotal
			item.Progress.TransferBytesDone = runItem.Progress.TransferBytesDone
			item.Detail = cloneTaskDetail(runItem.Detail)
			if err == nil {
				item.Detail["phase"] = "complete"
			}
		})
		return err
	}), nil
}

func removeMoveSource(ctx context.Context, source drivecopy.DriverCopySource, sourcePath string, isDir bool) error {
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

func cloneTaskDetail(detail map[string]any) map[string]any {
	if detail == nil {
		return nil
	}
	clone := make(map[string]any, len(detail))
	for key, value := range detail {
		clone[key] = value
	}
	return clone
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
