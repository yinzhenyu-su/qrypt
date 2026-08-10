package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// downloadReadBufferSize is the window used when streaming download source
// data. It matches DefaultReadChunkLimit so ReadAtInto never needs a custom
// limit, and it is large enough that a big file is read in a few dozen calls
// instead of one whole-file materialization.
const downloadReadBufferSize = 4 << 20

type downloadTaskSpec struct {
	Items       []task.Item
	LocalDirs   []string
	Overwrite   bool
	Recursive   bool
	Concurrency int
	localDirSet map[string]struct{}
}

func (c *Core) createDownloadTask(ctx context.Context, req task.Request) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	spec, err := downloadSpecFromTaskRequest(req)
	if err != nil {
		return task.Task{}, err
	}
	spec, err = c.expandDownloadTask(ctx, spec)
	if err != nil {
		return task.Task{}, err
	}
	now := timeutil.Now()
	first := task.Item{SourcePath: vfs.CleanVirtualPath(req.Items[0].SourcePath), DestPath: filepath.Clean(req.Items[0].DestPath)}
	if first.SourcePath == "/" && req.Items[0].Path != "" {
		first.SourcePath = vfs.CleanVirtualPath(req.Items[0].Path)
	}
	if len(spec.Items) > 0 {
		first = spec.Items[0]
	} else if len(spec.LocalDirs) > 0 {
		first.DestPath = spec.LocalDirs[0]
	}
	item := task.Task{
		ID:        newDownloadTaskID(),
		Type:      task.TypeDownload,
		State:     task.StateQueued,
		Scope:     task.ScopeUser,
		Path:      first.SourcePath,
		Name:      path.Base(first.SourcePath),
		CreatedAt: now,
		UpdatedAt: now,
		Progress: task.Progress{
			ItemsTotal: int64(len(spec.Items)),
		},
		Capabilities: task.Capabilities{
			Cancelable:  true,
			Persistent:  len(spec.Items) > 1 || spec.Recursive,
			Dismissible: len(spec.Items) > 1 || spec.Recursive,
		},
		Detail: map[string]any{
			"items":       downloadTaskDetailItems(spec.Items),
			"overwrite":   spec.Overwrite,
			"recursive":   spec.Recursive,
			"concurrency": spec.Concurrency,
			"phase":       "queued",
		},
	}
	sourceMount, _, _ := moveMounts(first.SourcePath, first.SourcePath, c.fs)
	if sourceMount != "" {
		item.Mount = sourceMount
		item.Detail["source_mount"] = sourceMount
	}
	manager, err := c.taskManager()
	if err != nil {
		return task.Task{}, err
	}
	return manager.Submit(ctx, item, func(runCtx context.Context, update task.UpdateFunc) error {
		return c.runDownloadTask(runCtx, update, spec)
	}), nil
}

func (c *Core) runDownloadTask(ctx context.Context, update task.UpdateFunc, spec downloadTaskSpec) error {
	for _, dir := range spec.LocalDirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := prepareDownloadDirectory(dir); err != nil {
			return err
		}
	}
	results := make([]task.ItemResult, len(spec.Items))
	var succeeded int64
	var bytesDone int64
	var bytesTotal int64
	var done int64
	var mu sync.Mutex
	active := map[int]string{}
	jobs := make(chan int)
	workers := taskConcurrency(spec.Concurrency)
	if workers > len(spec.Items) {
		workers = len(spec.Items)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				item := spec.Items[i]
				mu.Lock()
				active[i] = item.SourcePath
				activePaths := taskActivePaths(active)
				mu.Unlock()
				update(func(taskItem *task.Task) {
					taskItem.Progress.CurrentPath = item.SourcePath
					taskItem.Progress.Phase = "download"
					taskItem.Detail["phase"] = "download"
					taskItem.Detail["current_dest_path"] = item.DestPath
					taskItem.Detail["active_paths"] = activePaths
				})
				result, err := c.downloadOne(ctx, item, spec, func(delta, total int64) {
					if delta <= 0 {
						return
					}
					mu.Lock()
					bytesDone += delta
					currentBytesDone := bytesDone
					currentBytesTotal := bytesTotal + total
					mu.Unlock()
					update(func(taskItem *task.Task) {
						taskItem.Progress.OutputBytesDone = currentBytesDone
						taskItem.Progress.OutputBytesTotal = currentBytesTotal
					})
				})
				resultItem := task.ItemResult{
					Path:             item.SourcePath,
					SourcePath:       item.SourcePath,
					DestPath:         item.DestPath,
					State:            task.StateSucceeded,
					OutputBytesDone:  result.bytesDone,
					OutputBytesTotal: result.bytesTotal,
				}
				if err != nil {
					resultItem.State = task.StateFailed
					resultItem.Error = &task.Error{Message: err.Error()}
				}
				mu.Lock()
				delete(active, i)
				results[i] = resultItem
				done++
				bytesTotal += result.bytesTotal
				if err == nil {
					succeeded++
				}
				doneNow := done
				succeededNow := succeeded
				bytesNow := bytesDone
				totalNow := bytesTotal
				activePaths = taskActivePaths(active)
				resultSnapshot := compactItemResults(results)
				// Publish under the same lock that took the counters, so
				// concurrent workers emit monotonic progress (update order
				// == counter order; a stale snapshot can never win).
				update(func(taskItem *task.Task) {
					taskItem.Progress.ItemsDone = doneNow
					taskItem.Progress.ItemsFailed = doneNow - succeededNow
					taskItem.Progress.OutputBytesDone = bytesNow
					taskItem.Progress.OutputBytesTotal = totalNow
					taskItem.Result.Items = resultSnapshot
					taskItem.Detail["active_paths"] = activePaths
				})
				mu.Unlock()
			}
		}()
	}
	for i := range spec.Items {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	failedCount := len(results) - int(succeeded)
	if failedCount == 0 {
		update(func(taskItem *task.Task) {
			taskItem.Progress.CurrentPath = ""
			taskItem.Progress.Phase = "complete"
			taskItem.Detail["phase"] = "complete"
			taskItem.Detail["active_paths"] = []string{}
		})
		return nil
	}
	message := fmt.Sprintf("download failed for %d of %d items", failedCount, len(spec.Items))
	update(func(taskItem *task.Task) {
		taskItem.Progress.CurrentPath = ""
		taskItem.Detail["active_paths"] = []string{}
		taskItem.Error = &task.Error{Message: message, Retryable: true}
		taskItem.Capabilities.Retryable = true
		if succeeded > 0 {
			taskItem.State = task.StatePartialFailed
			taskItem.Progress.Phase = "partial_failed"
			taskItem.Detail["phase"] = "partial_failed"
		} else {
			taskItem.Progress.Phase = "failed"
			taskItem.Detail["phase"] = "failed"
		}
	})
	if succeeded > 0 {
		return nil
	}
	return fmt.Errorf("%s", message)
}

type downloadOneResult struct {
	bytesDone  int64
	bytesTotal int64
}

func (c *Core) downloadOne(ctx context.Context, item task.Item, spec downloadTaskSpec, progress func(delta, total int64)) (downloadOneResult, error) {
	entry, err := c.fs.Stat(ctx, item.SourcePath)
	if err != nil {
		return downloadOneResult{}, err
	}
	if entry.IsDir {
		return downloadOneResult{}, fmt.Errorf("core: download source %q is a directory; recursive download is required", item.SourcePath)
	}
	out := downloadOneResult{bytesTotal: entry.Size}
	if err := prepareDownloadDestination(item.DestPath, spec.Overwrite); err != nil {
		return out, err
	}
	if err := os.MkdirAll(filepath.Dir(item.DestPath), 0o755); err != nil {
		return out, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(item.DestPath), "."+filepath.Base(item.DestPath)+".*.part")
	if err != nil {
		return out, err
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	defer tmp.Close()

	if entry.Size > 0 {
		writer := &downloadProgressWriter{
			w:        tmp,
			total:    entry.Size,
			progress: progress,
		}
		// Stream in bounded windows instead of materializing the whole file in
		// one VFS.Read; this keeps memory flat and the VFS read cache warm in
		// window-sized pieces. ReadAtInto disables prefetch, which is correct
		// for a sequential download (window coalescing still drives one fetch
		// per window).
		buf := make([]byte, downloadReadBufferSize)
		var off int64
		for off < entry.Size {
			n, err := c.ReadAtInto(ctx, item.SourcePath, off, buf, 0)
			if err != nil {
				return out, err
			}
			if n == 0 {
				break
			}
			if _, err := writer.Write(buf[:n]); err != nil {
				return out, err
			}
			off += int64(n)
		}
		out.bytesDone = writer.done
	}
	if err := tmp.Close(); err != nil {
		return out, err
	}
	if spec.Overwrite {
		if err := removeExistingDownloadDestination(item.DestPath); err != nil {
			return out, err
		}
	}
	if err := os.Rename(tmpPath, item.DestPath); err != nil {
		return out, err
	}
	removeTmp = false
	return out, nil
}

func prepareDownloadDirectory(destPath string) error {
	info, err := os.Stat(destPath)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("core: download directory destination %q is a file", destPath)
		}
		return nil
	}
	if os.IsNotExist(err) {
		return os.MkdirAll(destPath, 0o755)
	}
	return err
}

type downloadProgressWriter struct {
	w        io.Writer
	total    int64
	done     int64
	progress func(delta, total int64)
}

func (w *downloadProgressWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if n > 0 {
		w.done += int64(n)
		w.progress(int64(n), w.total)
	}
	return n, err
}

func prepareDownloadDestination(destPath string, overwrite bool) error {
	info, err := os.Stat(destPath)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("core: download destination %q is a directory", destPath)
		}
		if !overwrite {
			return fmt.Errorf("core: download destination %q already exists", destPath)
		}
		return nil
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func removeExistingDownloadDestination(destPath string) error {
	info, err := os.Stat(destPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("core: download destination %q is a directory", destPath)
	}
	return os.Remove(destPath)
}

func downloadSpecFromTaskRequest(req task.Request) (downloadTaskSpec, error) {
	if len(req.Items) == 0 {
		return downloadTaskSpec{}, fmt.Errorf("core: download task requires at least one item")
	}
	spec := downloadTaskSpec{
		Overwrite:   req.Options.Overwrite,
		Recursive:   req.Options.Recursive,
		Concurrency: taskConcurrency(req.Options.Concurrency),
	}
	for _, item := range req.Items {
		sourcePath := item.SourcePath
		if sourcePath == "" {
			sourcePath = item.Path
		}
		sourcePath = vfs.CleanVirtualPath(sourcePath)
		if sourcePath == "/" {
			return downloadTaskSpec{}, fmt.Errorf("core: download source must be a file")
		}
		if item.DestPath == "" {
			return downloadTaskSpec{}, fmt.Errorf("core: download task item requires dest_path")
		}
		spec.Items = append(spec.Items, task.Item{SourcePath: sourcePath, DestPath: filepath.Clean(item.DestPath)})
	}
	return spec, nil
}

func (c *Core) expandDownloadTask(ctx context.Context, spec downloadTaskSpec) (downloadTaskSpec, error) {
	rawItems := spec.Items
	spec.Items = nil
	spec.localDirSet = make(map[string]struct{})
	for _, item := range rawItems {
		tree, err := walkTaskTree(ctx, c.fs, item.SourcePath)
		if err != nil {
			spec.Items = append(spec.Items, item)
			continue
		}
		if !tree.Root.IsDir {
			spec.Items = append(spec.Items, item)
			continue
		}
		if !spec.Recursive {
			spec.Items = append(spec.Items, item)
			continue
		}
		for _, dir := range tree.Dirs {
			spec.addLocalDir(filepath.Join(item.DestPath, filepath.FromSlash(relWithoutDot(dir.Rel))))
		}
		for _, file := range tree.Files {
			spec.Items = append(spec.Items, task.Item{
				SourcePath: file.Path,
				DestPath:   filepath.Join(item.DestPath, filepath.FromSlash(file.Rel)),
			})
		}
	}
	spec.localDirSet = nil
	return spec, nil
}

func (s *downloadTaskSpec) addLocalDir(dir string) {
	dir = filepath.Clean(dir)
	if s.localDirSet == nil {
		s.localDirSet = make(map[string]struct{})
	}
	if _, ok := s.localDirSet[dir]; ok {
		return
	}
	s.localDirSet[dir] = struct{}{}
	s.LocalDirs = append(s.LocalDirs, dir)
}

func downloadTaskDetailItems(items []task.Item) []map[string]string {
	out := make([]map[string]string, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]string{
			"source_path": item.SourcePath,
			"dest_path":   item.DestPath,
		})
	}
	return out
}

func newDownloadTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "download-" + strconv.FormatInt(timeutil.Now().UnixNano(), 36)
	}
	return "download-" + hex.EncodeToString(b[:])
}
