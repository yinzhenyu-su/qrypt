package core

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// coreFSService exposes the owner Core's filesystem to CLI data-plane clients.
// It is the transport bridge only: every operation lands on the same VFS,
// overlay and worker pool that FUSE and mobile use, so a CLI put/mv/list sees
// exactly the same pending projections as the mounted directory.
type coreFSService struct {
	core *Core
}

func fsEntryFromDrive(path string, entry drive.Entry) control.FSEntry {
	return control.FSEntry{
		ID:       entry.ID,
		ParentID: entry.ParentID,
		Name:     entry.Name,
		Path:     path,
		IsDir:    entry.IsDir,
		Size:     entry.Size,
		ModTime:  entry.ModTime,
	}
}

func (s coreFSService) fs() vfs.FileSystem {
	return s.core.fs
}

func (s coreFSService) Stat(ctx context.Context, path string) (control.FSEntry, error) {
	entry, err := s.fs().Stat(ctx, path)
	if err != nil {
		return control.FSEntry{}, err
	}
	return fsEntryFromDrive(path, entry), nil
}

func (s coreFSService) List(ctx context.Context, path string) ([]control.FSEntry, error) {
	entries, err := s.fs().List(ctx, path)
	if err != nil {
		return nil, err
	}
	result := make([]control.FSEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, fsEntryFromDrive(pathOf(path, entry.Name), entry))
	}
	return result, nil
}

func pathOf(parent, name string) string {
	if parent == "" || parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

func (s coreFSService) Read(ctx context.Context, path string, offset, size int64) ([]byte, error) {
	if size <= 0 {
		size = 4 << 20
	}
	reader, err := s.fs().Read(ctx, path, offset, size)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, size))
}

func (s coreFSService) Create(ctx context.Context, path string) error {
	return s.fs().Create(ctx, path)
}

func (s coreFSService) WriteAt(ctx context.Context, path string, offset int64, data []byte) (int, error) {
	return s.fs().WriteAt(ctx, path, data, offset)
}

func (s coreFSService) Flush(ctx context.Context, path string) error {
	return s.fs().Flush(ctx, path)
}

func (s coreFSService) Mkdir(ctx context.Context, path string) (control.FSEntry, error) {
	entry, err := s.fs().Mkdir(ctx, path)
	if err != nil {
		return control.FSEntry{}, err
	}
	return fsEntryFromDrive(path, entry), nil
}

func (s coreFSService) Remove(ctx context.Context, path string, dir bool) error {
	if dir {
		return s.fs().RemoveDir(ctx, path)
	}
	return s.fs().Remove(ctx, path)
}

func (s coreFSService) Rename(ctx context.Context, source, destination string) error {
	return s.fs().Rename(ctx, source, destination)
}

func (s coreFSService) Truncate(ctx context.Context, path string, size int64) error {
	return s.fs().Truncate(ctx, path, size)
}

var activeTaskStates = []task.State{
	task.StateQueued,
	task.StateScheduled,
	task.StateRunning,
	task.StateRetryWait,
	task.StateCanceling,
}

// WaitPath waits for one path to lose its pending upload work. Unlike the old
// process-local WaitFileSystemIdle it cannot be satisfied by unrelated activity
// in the shared runtime, and it reports permanent upload failures immediately
// instead of blocking until the caller's timeout.
// InvalidateList drops stale directory listings for path.
func (s coreFSService) InvalidateList(ctx context.Context, path string) error {
	type listInvalidator interface {
		InvalidateListCache(context.Context, string) error
	}
	fs, ok := s.core.fs.(listInvalidator)
	if !ok {
		return fmt.Errorf("core: list invalidation is unavailable")
	}
	return fs.InvalidateListCache(ctx, path)
}

func (s coreFSService) WaitPath(ctx context.Context, path string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		pendingForPath := 0
		if inspector, ok := s.fs().(vfs.UploadInspector); ok {
			for _, pending := range inspector.PendingUploads() {
				if pending.Path != path {
					continue
				}
				if pending.PermanentFail {
					return fmt.Errorf("core: upload of %s failed permanently: %s", path, pending.LastError)
				}
				pendingForPath++
			}
		}
		if pendingForPath == 0 && s.core != nil && s.core.tasks != nil {
			active, err := s.core.tasks.ListTasks(ctx, task.Filter{Path: path, States: activeTaskStates})
			if err != nil {
				return err
			}
			if len(active) == 0 {
				return nil
			}
		} else if pendingForPath == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			pending := []string{}
			if inspector, ok := s.fs().(vfs.UploadInspector); ok {
				for _, item := range inspector.PendingUploads() {
					pending = append(pending, item.Path+"("+item.FID+")")
				}
			}
			tasks := []string{}
			if s.core != nil && s.core.tasks != nil {
				if items, err := s.core.tasks.ListTasks(ctx, task.Filter{Path: path, States: activeTaskStates}); err == nil {
					for _, item := range items {
						tasks = append(tasks, item.ID+":"+string(item.State))
					}
				}
			}
			return fmt.Errorf("core: timed out waiting for %s to settle (pending=%v tasks=%v)", path, pending, tasks)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
