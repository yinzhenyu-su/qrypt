package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

const downloadStreamTaskPollInterval = 500 * time.Millisecond

type downloadStreamBatch struct {
	mu         sync.Mutex
	taskID     string
	items      []*downloadStreamItem
	byID       map[string]*downloadStreamItem
	bytesTotal int64
	update     task.UpdateFunc
	ready      chan struct{}
	readyOnce  sync.Once
	done       chan struct{}
	doneOnce   sync.Once
	canceled   bool
}

type downloadStreamItem struct {
	ID           string
	SourcePath   string
	Name         string
	RelativePath string
	Size         int64
	Acked        int64
	ReadOffset   int64
	Open         bool
	State        task.State
	Error        *task.Error
}

type DownloadStreamItemHandle struct {
	mu     sync.Mutex
	core   *Core
	batch  *downloadStreamBatch
	itemID string
	closed bool
}

func (c *Core) createDownloadStreamTask(ctx context.Context, req task.Request) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	batch, err := c.downloadStreamBatchFromRequest(ctx, req)
	if err != nil {
		return task.Task{}, err
	}
	now := timeutil.Now()
	first := batch.items[0]
	item := task.Task{
		ID:        batch.taskID,
		Type:      task.TypeDownloadStreamBatch,
		State:     task.StateQueued,
		Scope:     task.ScopeUser,
		Path:      first.SourcePath,
		Name:      first.Name,
		CreatedAt: now,
		UpdatedAt: now,
		Progress: task.Progress{
			ItemsTotal:       int64(len(batch.items)),
			OutputBytesTotal: batch.bytesTotal,
		},
		Capabilities: task.Capabilities{
			Cancelable:  true,
			Persistent:  true,
			Dismissible: true,
		},
		Detail: map[string]any{
			"items":       batch.detailItems(),
			"concurrency": taskConcurrency(req.Options.Concurrency),
			"phase":       "queued",
		},
		Result: task.Result{Items: batch.resultItemsLocked()},
	}
	sourceMount, _, _ := moveMounts(first.SourcePath, first.SourcePath, c.fs)
	if sourceMount != "" {
		item.Mount = sourceMount
		item.Detail["source_mount"] = sourceMount
	}
	c.putDownloadStream(batch)
	manager, err := c.taskManager()
	if err != nil {
		c.removeDownloadStream(batch.taskID)
		return task.Task{}, err
	}
	return manager.Submit(ctx, item, func(runCtx context.Context, update task.UpdateFunc) error {
		return c.runDownloadStreamTask(runCtx, update, batch)
	}), nil
}

func (c *Core) runDownloadStreamTask(ctx context.Context, update task.UpdateFunc, batch *downloadStreamBatch) error {
	batch.mu.Lock()
	batch.update = update
	batch.readyOnce.Do(func() { close(batch.ready) })
	batch.mu.Unlock()
	batch.updateTaskSnapshot()
	ticker := time.NewTicker(downloadStreamTaskPollInterval)
	defer ticker.Stop()
	defer c.removeDownloadStream(batch.taskID)
	for {
		select {
		case <-ctx.Done():
			batch.markCanceled(ctx.Err())
			batch.updateTaskSnapshot()
			return ctx.Err()
		case <-batch.done:
			return batch.finishTask(update)
		case <-ticker.C:
			batch.updateTaskSnapshot()
		}
	}
}

func (c *Core) OpenDownloadStreamItem(ctx context.Context, taskID, itemID string) (*DownloadStreamItemHandle, error) {
	batch := c.getDownloadStream(taskID)
	if batch == nil {
		return nil, fmt.Errorf("core: download stream task %q not active", taskID)
	}
	select {
	case <-batch.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	batch.mu.Lock()
	defer batch.mu.Unlock()
	if batch.canceled {
		return nil, fmt.Errorf("core: download stream task %q is canceled", taskID)
	}
	item := batch.byID[itemID]
	if item == nil {
		return nil, fmt.Errorf("core: download stream item %q not found", itemID)
	}
	if item.Open {
		return nil, fmt.Errorf("core: download stream item %q is already open", itemID)
	}
	if item.State == task.StateSucceeded {
		return nil, fmt.Errorf("core: download stream item %q already succeeded", itemID)
	}
	if item.State == task.StateCanceled || item.State == task.StateFailed {
		return nil, fmt.Errorf("core: download stream item %q is %s", itemID, item.State)
	}
	item.Open = true
	item.State = task.StateRunning
	item.ReadOffset = item.Acked
	item.Error = nil
	batch.updateTaskSnapshotLocked()
	return &DownloadStreamItemHandle{core: c, batch: batch, itemID: itemID}, nil
}

func (h *DownloadStreamItemHandle) ReadInto(ctx context.Context, dst []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(dst) == 0 {
		return 0, nil
	}
	h.batch.mu.Lock()
	item, err := h.itemLocked()
	if err != nil {
		h.batch.mu.Unlock()
		return 0, err
	}
	sourcePath := item.SourcePath
	readOffset := item.ReadOffset
	size := item.Size
	h.batch.mu.Unlock()
	if readOffset >= size {
		return 0, nil
	}
	if maxLen := size - readOffset; int64(len(dst)) > maxLen {
		dst = dst[:maxLen]
	}
	n, err := h.core.ReadAtInto(vfs.WithoutReadPrefetch(ctx), sourcePath, readOffset, dst, 0)
	h.batch.mu.Lock()
	if current := h.batch.byID[h.itemID]; current != nil && !h.closed && n > 0 {
		current.ReadOffset += int64(n)
	}
	h.batch.mu.Unlock()
	return n, err
}

func (h *DownloadStreamItemHandle) Ack(bytesWritten int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if bytesWritten < 0 {
		return fmt.Errorf("core: ack bytes must be non-negative")
	}
	h.batch.mu.Lock()
	defer h.batch.mu.Unlock()
	item, err := h.itemLocked()
	if err != nil {
		return err
	}
	if item.Acked+bytesWritten > item.ReadOffset {
		return fmt.Errorf("core: ack exceeds read offset for item %q", item.ID)
	}
	item.Acked += bytesWritten
	item.State = task.StateRunning
	item.Error = nil
	h.batch.updateTaskSnapshotLocked()
	return nil
}

func (h *DownloadStreamItemHandle) Commit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.batch.mu.Lock()
	defer h.batch.mu.Unlock()
	item, err := h.itemLocked()
	if err != nil {
		return err
	}
	if item.Acked != item.Size {
		return fmt.Errorf("core: download stream item %q acked %d of %d bytes", item.ID, item.Acked, item.Size)
	}
	item.Open = false
	item.State = task.StateSucceeded
	item.Error = nil
	h.closed = true
	h.batch.updateTaskSnapshotLocked()
	h.batch.closeDoneIfTerminalLocked()
	return nil
}

func (h *DownloadStreamItemHandle) Fail(code, message string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.batch.mu.Lock()
	defer h.batch.mu.Unlock()
	item, err := h.itemLocked()
	if err != nil {
		return err
	}
	if message == "" {
		message = "output stream failed"
	}
	item.Open = false
	item.State = task.StateWaitingOutput
	item.ReadOffset = item.Acked
	item.Error = &task.Error{Code: code, Message: message, Retryable: true}
	h.closed = true
	h.batch.updateTaskSnapshotLocked()
	return nil
}

func (h *DownloadStreamItemHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.batch.mu.Lock()
	defer h.batch.mu.Unlock()
	if h.closed {
		return nil
	}
	item := h.batch.byID[h.itemID]
	if item != nil {
		item.Open = false
		if item.State == task.StateRunning {
			item.State = task.StateWaitingOutput
			item.ReadOffset = item.Acked
		}
	}
	h.closed = true
	h.batch.updateTaskSnapshotLocked()
	return nil
}

func (h *DownloadStreamItemHandle) itemLocked() (*downloadStreamItem, error) {
	if h.closed {
		return nil, fmt.Errorf("core: download stream item handle is closed")
	}
	item := h.batch.byID[h.itemID]
	if item == nil {
		return nil, fmt.Errorf("core: download stream item %q not found", h.itemID)
	}
	if item.State == task.StateCanceled || item.State == task.StateFailed {
		return nil, fmt.Errorf("core: download stream item %q is %s", h.itemID, item.State)
	}
	return item, nil
}

func (c *Core) cancelDownloadStreamItem(ctx context.Context, batch *downloadStreamBatch, itemID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	batch.mu.Lock()
	defer batch.mu.Unlock()
	if batch.canceled {
		return fmt.Errorf("core: download stream task %q is canceled", batch.taskID)
	}
	item := batch.byID[itemID]
	if item == nil {
		return fmt.Errorf("core: download stream item %q not found", itemID)
	}
	if item.State == task.StateSucceeded || item.State == task.StateCanceled {
		return nil
	}
	item.Open = false
	item.ReadOffset = item.Acked
	item.State = task.StateCanceled
	item.Error = &task.Error{Code: "canceled", Message: "task item canceled"}
	batch.updateTaskSnapshotLocked()
	batch.closeDoneIfTerminalLocked()
	return nil
}

func (c *Core) downloadStreamBatchFromRequest(ctx context.Context, req task.Request) (*downloadStreamBatch, error) {
	if len(req.Items) == 0 {
		return nil, fmt.Errorf("core: download stream task requires at least one item")
	}
	batch := &downloadStreamBatch{
		taskID: newDownloadStreamTaskID(),
		byID:   map[string]*downloadStreamItem{},
		ready:  make(chan struct{}),
		done:   make(chan struct{}),
	}
	for i, reqItem := range req.Items {
		sourcePath := reqItem.SourcePath
		if sourcePath == "" {
			sourcePath = reqItem.Path
		}
		sourcePath = vfs.CleanVirtualPath(sourcePath)
		if sourcePath == "/" {
			return nil, fmt.Errorf("core: download stream source must be a file")
		}
		entry, err := c.fs.Stat(ctx, sourcePath)
		if err != nil {
			return nil, err
		}
		if entry.IsDir {
			return nil, fmt.Errorf("core: download stream source %q is a directory; expand directories in the app or use path download", sourcePath)
		}
		itemID := reqItem.ItemID
		if itemID == "" {
			itemID = "item-" + strconv.Itoa(i+1)
		}
		if batch.byID[itemID] != nil {
			return nil, fmt.Errorf("core: duplicate download stream item_id %q", itemID)
		}
		name := reqItem.Name
		if name == "" {
			name = path.Base(sourcePath)
		}
		size := reqItem.Size
		if size <= 0 {
			size = entry.Size
		}
		item := &downloadStreamItem{
			ID:           itemID,
			SourcePath:   sourcePath,
			Name:         name,
			RelativePath: reqItem.RelativePath,
			Size:         size,
			State:        task.StateQueued,
		}
		batch.items = append(batch.items, item)
		batch.byID[itemID] = item
		batch.bytesTotal += size
	}
	return batch, nil
}

func (b *downloadStreamBatch) detailItems() []map[string]any {
	out := make([]map[string]any, 0, len(b.items))
	for _, item := range b.items {
		out = append(out, map[string]any{
			"item_id":       item.ID,
			"source_path":   item.SourcePath,
			"name":          item.Name,
			"relative_path": item.RelativePath,
			"size":          item.Size,
		})
	}
	return out
}

func (b *downloadStreamBatch) resultItemsLocked() []task.ItemResult {
	out := make([]task.ItemResult, 0, len(b.items))
	for _, item := range b.items {
		out = append(out, task.ItemResult{
			Path:             item.SourcePath,
			ItemID:           item.ID,
			SourcePath:       item.SourcePath,
			State:            item.State,
			Error:            cloneTaskError(item.Error),
			OutputBytesDone:  item.Acked,
			OutputBytesTotal: item.Size,
			ResumeOffset:     item.Acked,
			Capabilities:     downloadStreamItemCapabilities(item),
		})
	}
	return out
}

func downloadStreamItemCapabilities(item *downloadStreamItem) task.ItemCapabilities {
	return task.ItemCapabilities{
		OpenOutput: item.State == task.StateQueued || item.State == task.StateWaitingOutput,
		Cancelable: item.State != task.StateSucceeded &&
			item.State != task.StateFailed &&
			item.State != task.StateCanceled,
	}
}

func (b *downloadStreamBatch) updateTaskSnapshot() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.updateTaskSnapshotLocked()
}

func (b *downloadStreamBatch) updateTaskSnapshotLocked() {
	if b.update == nil {
		return
	}
	itemsDone, itemsFailed, bytesDone, phase, active := b.summaryLocked()
	results := b.resultItemsLocked()
	b.update(func(taskItem *task.Task) {
		taskItem.Progress.ItemsDone = itemsDone
		taskItem.Progress.ItemsFailed = itemsFailed
		taskItem.Progress.OutputBytesDone = bytesDone
		taskItem.Progress.OutputBytesTotal = b.bytesTotal
		taskItem.Progress.Phase = phase
		taskItem.Progress.CurrentPath = ""
		if len(active) > 0 {
			taskItem.Progress.CurrentPath = active[0]
		}
		taskItem.Detail["phase"] = phase
		taskItem.Detail["active_paths"] = active
		taskItem.Result.Items = results
	})
}

func (b *downloadStreamBatch) summaryLocked() (itemsDone, itemsFailed, bytesDone int64, phase string, active []string) {
	phase = "download"
	waiting := false
	for _, item := range b.items {
		bytesDone += item.Acked
		switch item.State {
		case task.StateSucceeded:
			itemsDone++
		case task.StateFailed, task.StateCanceled:
			itemsDone++
			itemsFailed++
		case task.StateWaitingOutput:
			waiting = true
		case task.StateRunning:
			active = append(active, item.SourcePath)
		}
	}
	if waiting {
		phase = string(task.StateWaitingOutput)
	}
	if itemsDone == int64(len(b.items)) && itemsFailed == 0 {
		phase = "complete"
	}
	if itemsDone == int64(len(b.items)) && itemsFailed > 0 {
		if itemsFailed == int64(len(b.items)) {
			phase = "failed"
		} else {
			phase = "partial_failed"
		}
	}
	return itemsDone, itemsFailed, bytesDone, phase, active
}

func (b *downloadStreamBatch) finishTask(update task.UpdateFunc) error {
	b.mu.Lock()
	itemsDone, itemsFailed, _, _, _ := b.summaryLocked()
	results := b.resultItemsLocked()
	b.mu.Unlock()
	if itemsDone == int64(len(b.items)) && itemsFailed == 0 {
		update(func(taskItem *task.Task) {
			taskItem.Progress.CurrentPath = ""
			taskItem.Progress.Phase = "complete"
			taskItem.Detail["phase"] = "complete"
			taskItem.Detail["active_paths"] = []string{}
			taskItem.Result.Items = results
		})
		return nil
	}
	message := fmt.Sprintf("download stream failed for %d of %d items", itemsFailed, len(b.items))
	update(func(taskItem *task.Task) {
		taskItem.Progress.CurrentPath = ""
		taskItem.Detail["active_paths"] = []string{}
		taskItem.Error = &task.Error{Message: message, Retryable: true}
		taskItem.Capabilities.Retryable = true
		taskItem.Result.Items = results
		if itemsFailed < int64(len(b.items)) {
			taskItem.State = task.StatePartialFailed
			taskItem.Progress.Phase = "partial_failed"
			taskItem.Detail["phase"] = "partial_failed"
		} else {
			taskItem.Progress.Phase = "failed"
			taskItem.Detail["phase"] = "failed"
		}
	})
	if itemsFailed < int64(len(b.items)) {
		return nil
	}
	return fmt.Errorf("%s", message)
}

func (b *downloadStreamBatch) markCanceled(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.canceled = true
	for _, item := range b.items {
		if item.State == task.StateSucceeded || item.State == task.StateFailed {
			continue
		}
		item.Open = false
		item.State = task.StateFailed
		item.Error = &task.Error{Code: "canceled", Message: err.Error()}
	}
}

func (b *downloadStreamBatch) closeDoneIfTerminalLocked() {
	for _, item := range b.items {
		if item.State != task.StateSucceeded && item.State != task.StateFailed && item.State != task.StateCanceled {
			return
		}
	}
	b.doneOnce.Do(func() { close(b.done) })
}

func (c *Core) putDownloadStream(batch *downloadStreamBatch) {
	c.streamsMu.Lock()
	defer c.streamsMu.Unlock()
	if c.downloadStreams == nil {
		c.downloadStreams = map[string]*downloadStreamBatch{}
	}
	c.downloadStreams[batch.taskID] = batch
}

func (c *Core) getDownloadStream(taskID string) *downloadStreamBatch {
	c.streamsMu.Lock()
	defer c.streamsMu.Unlock()
	return c.downloadStreams[taskID]
}

func (c *Core) removeDownloadStream(taskID string) {
	c.streamsMu.Lock()
	defer c.streamsMu.Unlock()
	delete(c.downloadStreams, taskID)
}

func cloneTaskError(err *task.Error) *task.Error {
	if err == nil {
		return nil
	}
	clone := *err
	return &clone
}

func newDownloadStreamTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "download-stream-" + strconv.FormatInt(timeutil.Now().UnixNano(), 36)
	}
	return "download-stream-" + hex.EncodeToString(b[:])
}
