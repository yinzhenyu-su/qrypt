package core

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"strconv"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type directUploadBackend interface {
	SupportsSourceUpload(path string) bool
	UploadSource(ctx context.Context, path string, req vfs.SourceUploadRequest) (drive.Entry, error)
}

type uploadStreamDirectProgress struct {
	batch  *uploadStreamBatch
	itemID string
}

func (c *Core) SetUploadSourceProvider(provider UploadSourceProvider) {
	if c == nil {
		return
	}
	c.streamsMu.Lock()
	c.uploadSources = provider
	c.streamsMu.Unlock()
}

func (c *Core) createUploadStreamDirectTask(ctx context.Context, req task.Request) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	batch, err := c.uploadStreamBatchFromRequest(ctx, req)
	if err != nil {
		return task.Task{}, err
	}
	batch.channel = "direct"
	for i, reqItem := range req.Items {
		if reqItem.SourcePath == "" {
			return task.Task{}, fmt.Errorf("core: direct upload stream item requires source_path")
		}
		batch.items[i].SourceToken = reqItem.SourcePath
	}
	now := util.Now()
	first := batch.items[0]
	item := task.Task{
		ID:        batch.taskID,
		Type:      task.TypeUploadStreamDirect,
		State:     task.StateQueued,
		Scope:     task.ScopeUser,
		Path:      first.DestPath,
		Name:      first.Name,
		CreatedAt: now,
		UpdatedAt: now,
		Progress: task.Progress{
			ItemsTotal:        int64(len(batch.items)),
			CloudBytesTotal:   batch.bytesTotal,
			StagingBytesTotal: 0,
		},
		Capabilities: task.Capabilities{
			Cancelable:  true,
			Persistent:  true,
			Dismissible: true,
		},
		Detail: map[string]any{
			"items":           batch.detailItems(),
			"channel":         "direct",
			"conflict_policy": batch.conflictPolicy,
			"phase":           "queued",
		},
		Result: task.Result{Items: batch.resultItemsLocked()},
	}
	destMount, _, _ := moveMounts(first.DestPath, first.DestPath, c.fs)
	if destMount != "" {
		item.Mount = destMount
		item.Detail["dest_mount"] = destMount
	}
	c.putUploadStream(batch)
	manager, err := c.taskManager()
	if err != nil {
		c.removeUploadStream(batch.taskID)
		return task.Task{}, err
	}
	return manager.Submit(ctx, item, func(runCtx context.Context, update task.UpdateFunc) error {
		return c.runUploadStreamDirectTask(runCtx, update, batch)
	}), nil
}

func (c *Core) recoverUploadStreamDirectTasks(ctx context.Context, manager *task.Manager) {
	if c == nil || manager == nil {
		return
	}
	tasks, err := manager.ListTasks(ctx, task.Filter{Types: []task.Type{task.TypeUploadStreamDirect}})
	if err != nil {
		return
	}
	for _, item := range tasks {
		if !isInterruptedUploadStreamDirectTask(item) {
			continue
		}
		batch, err := c.uploadStreamDirectBatchFromTask(item)
		if err != nil {
			continue
		}
		c.putUploadStream(batch)
		if _, ok, err := manager.RecoverTask(ctx, item.ID, func(runCtx context.Context, update task.UpdateFunc) error {
			return c.runUploadStreamDirectTask(runCtx, update, batch)
		}); err != nil || !ok {
			c.removeUploadStream(batch.taskID)
		}
	}
}

func isInterruptedUploadStreamDirectTask(item task.Task) bool {
	return item.Type == task.TypeUploadStreamDirect &&
		item.State == task.StateFailed &&
		item.Error != nil &&
		item.Error.Code == "interrupted"
}

func (c *Core) uploadStreamDirectBatchFromTask(item task.Task) (*uploadStreamBatch, error) {
	detailItems, ok := directUploadDetailItems(item.Detail["items"])
	if !ok || len(detailItems) == 0 {
		return nil, fmt.Errorf("core: direct upload task %q missing items", item.ID)
	}
	conflictPolicy, _ := item.Detail["conflict_policy"].(string)
	if conflictPolicy == "" {
		conflictPolicy = "overwrite"
	}
	batch := &uploadStreamBatch{
		taskID:         item.ID,
		byID:           map[string]*uploadStreamItem{},
		channel:        "direct",
		conflictPolicy: conflictPolicy,
		ready:          make(chan struct{}),
		done:           make(chan struct{}),
	}
	for i, detail := range detailItems {
		itemID := directUploadDetailString(detail, "item_id")
		if itemID == "" {
			itemID = "item-" + strconv.Itoa(i+1)
		}
		if batch.byID[itemID] != nil {
			return nil, fmt.Errorf("core: duplicate upload stream item_id %q", itemID)
		}
		destPath := directUploadDetailString(detail, "dest_path")
		if destPath == "" {
			return nil, fmt.Errorf("core: direct upload task %q item %q missing dest_path", item.ID, itemID)
		}
		sourceToken := directUploadDetailString(detail, "source_path")
		if sourceToken == "" {
			return nil, fmt.Errorf("core: direct upload task %q item %q missing source_path", item.ID, itemID)
		}
		size := directUploadDetailInt64(detail, "size")
		streamItem := &uploadStreamItem{
			ID:           itemID,
			DestPath:     destPath,
			Name:         directUploadDetailString(detail, "name"),
			RelativePath: directUploadDetailString(detail, "relative_path"),
			SourceToken:  sourceToken,
			Size:         size,
			State:        task.StateWaitingInput,
		}
		if streamItem.Name == "" {
			streamItem.Name = path.Base(destPath)
		}
		batch.items = append(batch.items, streamItem)
		batch.byID[itemID] = streamItem
		batch.bytesTotal += size
	}
	return batch, nil
}

func directUploadDetailItems(value any) ([]map[string]any, bool) {
	switch items := value.(type) {
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, item)
		}
		return out, true
	case []map[string]any:
		return items, true
	default:
		return nil, false
	}
}

func directUploadDetailString(detail map[string]any, key string) string {
	value, _ := detail[key].(string)
	return value
}

func directUploadDetailInt64(detail map[string]any, key string) int64 {
	switch value := detail[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func (c *Core) runUploadStreamDirectTask(ctx context.Context, update task.UpdateFunc, batch *uploadStreamBatch) error {
	batch.mu.Lock()
	batch.update = update
	batch.readyOnce.Do(func() { close(batch.ready) })
	batch.mu.Unlock()
	defer c.removeUploadStream(batch.taskID)
	batch.updateTaskSnapshot()
	for _, snapshot := range batch.itemsSnapshot() {
		if snapshot.State == task.StateSucceeded {
			continue
		}
		if err := c.uploadStreamDirectItem(ctx, batch, snapshot.ID); err != nil && ctx.Err() != nil {
			batch.markCanceled(ctx.Err())
			batch.updateTaskSnapshot()
			return ctx.Err()
		}
	}
	batch.mu.Lock()
	batch.closeDoneIfTerminalLocked()
	batch.mu.Unlock()
	return batch.finishTask(update)
}

func (c *Core) uploadStreamDirectItem(ctx context.Context, batch *uploadStreamBatch, itemID string) error {
	batch.mu.Lock()
	item := batch.byID[itemID]
	if item == nil {
		batch.mu.Unlock()
		return fmt.Errorf("core: upload stream item %q not found", itemID)
	}
	token := item.SourceToken
	destPath := item.DestPath
	size := item.Size
	item.Open = false
	item.State = task.StateRunning
	item.CloudPhase = "hashing"
	item.Error = nil
	batch.updateTaskSnapshotLocked()
	batch.mu.Unlock()

	source := c.newDirectUploadSource(token, size)
	if err := source.computeHashes(ctx); err != nil {
		c.failUploadStreamDirectItem(batch, itemID, err)
		return err
	}
	batch.mu.Lock()
	if current := batch.byID[itemID]; current != nil {
		current.CloudPhase = "uploading"
	}
	batch.updateTaskSnapshotLocked()
	batch.mu.Unlock()

	entry, direct, err := c.uploadDirectOrFallback(ctx, destPath, source, uploadStreamDirectProgress{batch: batch, itemID: itemID})
	batch.mu.Lock()
	defer batch.mu.Unlock()
	item = batch.byID[itemID]
	if item == nil {
		return err
	}
	if err != nil {
		item.Open = false
		item.State = task.StateFailed
		item.Error = &task.Error{Message: err.Error(), Retryable: true}
		batch.updateTaskSnapshotLocked()
		return err
	}
	item.Open = false
	item.State = task.StateSucceeded
	item.Error = nil
	item.RemoteID = entry.ID
	item.Written = 0
	item.CloudWritten = entry.Size
	item.CloudTotal = entry.Size
	if direct {
		if item.CloudPhase == "" || item.CloudPhase == string(drive.UploadPhaseUploading) || item.CloudPhase == string(drive.UploadPhaseCompleted) {
			item.CloudPhase = "direct"
		}
	} else {
		item.CloudPhase = "queued_upload"
	}
	batch.updateTaskSnapshotLocked()
	return nil
}

func (c *Core) uploadDirectOrFallback(ctx context.Context, destPath string, source *directUploadSource, progress drive.UploadProgress) (drive.Entry, bool, error) {
	if backend, ok := c.fs.(directUploadBackend); ok && backend.SupportsSourceUpload(destPath) {
		entry, err := backend.UploadSource(ctx, destPath, vfs.SourceUploadRequest{
			Source:   source,
			Progress: progress,
		})
		return entry, true, err
	}
	entry, err := c.uploadSourceViaStaging(ctx, destPath, source)
	return entry, false, err
}

func (c *Core) uploadSourceViaStaging(ctx context.Context, destPath string, source *directUploadSource) (drive.Entry, error) {
	service, err := c.UploadService()
	if err != nil {
		return drive.Entry{}, err
	}
	if err := service.BeginStream(ctx, destPath); err != nil {
		return drive.Entry{}, err
	}
	reader, err := source.Open(ctx)
	if err != nil {
		_ = service.CancelStream(context.WithoutCancel(ctx), destPath)
		return drive.Entry{}, err
	}
	defer reader.Close()
	buf := make([]byte, uploadCopyChunkSize)
	var off int64
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			written, err := service.WriteStream(ctx, destPath, buf[:n], off)
			if err != nil {
				_ = service.CancelStream(context.WithoutCancel(ctx), destPath)
				return drive.Entry{}, err
			}
			if written != n {
				_ = service.CancelStream(context.WithoutCancel(ctx), destPath)
				return drive.Entry{}, fmt.Errorf("core: short staging write: wrote %d of %d", written, n)
			}
			off += int64(written)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = service.CancelStream(context.WithoutCancel(ctx), destPath)
			return drive.Entry{}, readErr
		}
	}
	entry, err := service.FinishStream(ctx, destPath)
	if err != nil {
		return drive.Entry{}, err
	}
	remoteTask, hasRemoteTask, err := c.waitUploadTaskForPath(ctx, destPath)
	if err != nil {
		return drive.Entry{}, err
	}
	if finalEntry, statErr := c.fs.Stat(ctx, destPath); statErr == nil {
		entry = finalEntry
	}
	if hasRemoteTask {
		result := UploadResult{Entry: entry}
		result.applyRemoteTask(remoteTask)
		entry = result.Entry
	}
	return entry, nil
}

func (c *Core) failUploadStreamDirectItem(batch *uploadStreamBatch, itemID string, err error) {
	batch.mu.Lock()
	defer batch.mu.Unlock()
	if item := batch.byID[itemID]; item != nil {
		item.Open = false
		item.State = task.StateFailed
		item.Error = &task.Error{Message: err.Error(), Retryable: true}
	}
	batch.updateTaskSnapshotLocked()
}

func (p uploadStreamDirectProgress) Phase(phase drive.UploadPhase) {
	p.batch.mu.Lock()
	defer p.batch.mu.Unlock()
	if item := p.batch.byID[p.itemID]; item != nil {
		item.CloudPhase = string(phase)
	}
	p.batch.updateTaskSnapshotLocked()
}

func (p uploadStreamDirectProgress) Uploaded(n int64) {
	if n <= 0 {
		return
	}
	p.batch.mu.Lock()
	defer p.batch.mu.Unlock()
	if item := p.batch.byID[p.itemID]; item != nil {
		item.CloudWritten += n
		if item.CloudTotal == 0 {
			item.CloudTotal = item.Size
		}
	}
	p.batch.updateTaskSnapshotLocked()
}

type directUploadSource struct {
	provider UploadSourceProvider
	token    string
	size     int64
	hashes   drive.SourceHashes
}

func (c *Core) newDirectUploadSource(token string, size int64) *directUploadSource {
	c.streamsMu.Lock()
	provider := c.uploadSources
	c.streamsMu.Unlock()
	if provider == nil {
		provider = localUploadSourceProvider{}
	}
	return &directUploadSource{provider: provider, token: token, size: size}
}

func (s *directUploadSource) Size() int64 {
	return s.size
}

func (s *directUploadSource) Open(ctx context.Context) (drive.ReadOnlyFile, error) {
	return &directUploadFile{ctx: ctx, provider: s.provider, token: s.token}, nil
}

func (s *directUploadSource) Hash(algorithm drive.HashAlgorithm) ([]byte, bool) {
	sum, ok := s.hashes[algorithm]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), sum...), true
}

func (s *directUploadSource) computeHashes(ctx context.Context) error {
	reader, err := s.Open(ctx)
	if err != nil {
		return err
	}
	defer reader.Close()
	hashes := []struct {
		algorithm drive.HashAlgorithm
		hash      hash.Hash
	}{
		{drive.HashMD5, md5.New()},
		{drive.HashSHA1, sha1.New()},
		{drive.HashSHA256, sha256.New()},
	}
	writers := make([]io.Writer, 0, len(hashes))
	for _, item := range hashes {
		writers = append(writers, item.hash)
	}
	if _, err := io.Copy(io.MultiWriter(writers...), reader); err != nil {
		return err
	}
	s.hashes = drive.SourceHashes{}
	for _, item := range hashes {
		s.hashes[item.algorithm] = item.hash.Sum(nil)
	}
	return nil
}

type directUploadFile struct {
	ctx      context.Context
	provider UploadSourceProvider
	token    string
	mu       sync.Mutex
	reader   io.ReadCloser
	offset   int64
}

func (f *directUploadFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reader == nil {
		reader, err := f.provider.OpenUploadSource(f.ctx, f.token, f.offset)
		if err != nil {
			return 0, err
		}
		f.reader = reader
	}
	n, err := f.reader.Read(p)
	f.offset += int64(n)
	return n, err
}

func (f *directUploadFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("core: offset must be non-negative")
	}
	reader, err := f.provider.OpenUploadSource(f.ctx, f.token, off)
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	n, err := io.ReadFull(reader, p)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	return n, err
}

func (f *directUploadFile) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += f.offset
	default:
		return 0, fmt.Errorf("core: unsupported seek mode %d", whence)
	}
	if offset < 0 {
		return 0, fmt.Errorf("core: offset must be non-negative")
	}
	if f.reader != nil {
		_ = f.reader.Close()
		f.reader = nil
	}
	f.offset = offset
	return f.offset, nil
}

func (f *directUploadFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reader == nil {
		return nil
	}
	err := f.reader.Close()
	f.reader = nil
	return err
}

type localUploadSourceProvider struct{}

func (localUploadSourceProvider) OpenUploadSource(ctx context.Context, token string, offset int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(token)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return file, nil
}

var _ drive.ReadOnlyFileSource = (*directUploadSource)(nil)
var _ drive.HashProvider = (*directUploadSource)(nil)
var _ drive.ReadOnlyFile = (*directUploadFile)(nil)
