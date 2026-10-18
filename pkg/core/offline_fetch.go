package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// offlineURLRefreshes bounds how many times one file transfer re-obtains an
// expired provider download URL before giving up.
const offlineURLRefreshes = 5

// offlineTransferChunk is the read buffer used while copying a response body.
const offlineTransferChunk = 256 * 1024

// Provider download URLs are always requested with a bounded `bytes=start-end`
// range. Quark's CDN serves a request without one - a plain GET, or the
// open-ended `bytes=start-` a resume would otherwise send - at roughly 100 KB/s
// while a bounded range on the very same URL runs at full CDN speed, so a
// transfer that wants throughput has to chunk even its first request.
const (
	offlineFetchChunk   = 8 * 1024 * 1024
	offlineFetchWorkers = 4
)

// offlineHTTPClient streams provider download URLs. It has no total timeout:
// the transfer context bounds the request, and a signed URL can legitimately
// serve a large file for a long time.
var offlineHTTPClient = &http.Client{Transport: http.DefaultTransport}

// transferPhase brings the staged result down to the final destination.
func (r *offlineRun) transferPhase(ctx context.Context, update task.UpdateFunc) error {
	targets, asDirectory, err := r.fetchTargets(ctx)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("core: offline fetch has no files to transfer")
	}
	if err := r.ensureDownloadDirectory(ctx, targets, asDirectory); err != nil {
		return err
	}
	var total int64
	for _, target := range targets {
		total += target.size
	}
	update(func(item *task.Task) {
		item.Progress.Phase = offlinePhaseTransfer.display()
		item.Progress.TransferBytesTotal = total
	})
	var done int64
	for _, target := range targets {
		written, err := r.transferFile(ctx, update, target, done)
		if err != nil {
			return err
		}
		done += written
	}
	return nil
}

type offlineFetchTarget struct {
	file   offlineResultFile
	target string
	size   int64
}

// fetchTargets resolves which provider files map to which destination paths.
// The destination is treated as a directory when more than one file is
// selected, when it names an existing directory, or when it ends in a path
// separator; each file is then written under it by name. Otherwise a single
// file is written to the destination path itself.
func (r *offlineRun) fetchTargets(ctx context.Context) ([]offlineFetchTarget, bool, error) {
	files := r.detail.ResultFiles
	if len(files) == 0 {
		// Never infer ownership from whatever happens to be in the parse
		// directory: unrelated entries there must not be fetched or deleted.
		// The provider's own result list is the only authority.
		return nil, false, nil
	}
	asDirectory := len(files) > 1 || r.downloadDestinationIsDirectory(ctx)
	base := r.detail.DownloadTo
	if r.detail.DownloadMount != "" {
		// The caller may have named the destination through the Core's own
		// FUSE tree, so use the mapped namespace path rather than the native
		// one for Core operations.
		base = r.destinationRoot()
	}
	targets := make([]offlineFetchTarget, 0, len(files))
	for _, file := range files {
		destination := base
		if asDirectory {
			name := file.Name
			if name == "" {
				name = offlineResultItemID(file.Index, file)
			}
			if r.detail.DownloadMount != "" {
				destination = joinNamespaceDestination(base, name)
			} else {
				destination = joinDestination(base, name)
			}
		}
		targets = append(targets, offlineFetchTarget{file: file, target: destination, size: file.Size})
	}
	return targets, asDirectory, nil
}

// downloadDestinationIsDirectory reports whether the caller's final
// destination names a directory rather than a single file.
func (r *offlineRun) downloadDestinationIsDirectory(ctx context.Context) bool {
	destination := strings.TrimSpace(r.detail.DownloadTo)
	if destination == "" {
		return false
	}
	if strings.HasSuffix(destination, "/") || strings.HasSuffix(destination, string(os.PathSeparator)) {
		return true
	}
	if r.detail.DownloadMount != "" {
		if entry, err := r.core.fs.Stat(ctx, r.destinationRoot()); err == nil {
			return entry.IsDir
		}
		return false
	}
	if info, err := os.Stat(destination); err == nil {
		return info.IsDir()
	}
	return false
}

// joinDestination joins a local directory destination and a name with native
// path rules, so a relative destination such as "./downloads/" stays relative
// and a Windows path keeps its own separator.
func joinDestination(destination, name string) string {
	return filepath.Join(destination, name)
}

// joinNamespaceDestination joins a virtual destination and a name with
// namespace rules, independent of the host OS.
func joinNamespaceDestination(destination, name string) string {
	return vfstypes.JoinVirtualPath(vfstypes.CleanVirtualPath(destination), name)
}

// ensureDownloadDirectory creates the destination directory when the transfer
// writes into one, and otherwise only the single file's parent. A missing
// destination directory is created automatically, unlike a missing parse
// destination.
func (r *offlineRun) ensureDownloadDirectory(ctx context.Context, targets []offlineFetchTarget, asDirectory bool) error {
	if !asDirectory {
		// A single file target: create the parent directory only.
		return r.ensureParentDirectory(ctx, targets[0].target)
	}
	if r.detail.DownloadMount != "" {
		path := r.destinationRoot()
		if _, err := r.core.fs.Stat(ctx, path); err != nil {
			if !vfstypes.IsNotFound(err) {
				return err
			}
			if _, err := r.core.fs.Mkdir(ctx, path); err != nil {
				return fmt.Errorf("core: create offline download directory %s: %w", path, err)
			}
		}
		return nil
	}
	if err := os.MkdirAll(r.detail.DownloadTo, 0o755); err != nil {
		return fmt.Errorf("core: create offline download directory %s: %w", r.detail.DownloadTo, err)
	}
	return nil
}

func (r *offlineRun) ensureParentDirectory(ctx context.Context, target string) error {
	if r.detail.DownloadMount != "" {
		parent := vfstypes.CleanVirtualPath(target)
		if idx := strings.LastIndex(parent, "/"); idx > 0 {
			parent = parent[:idx]
		}
		if parent == "" || parent == "/" {
			return nil
		}
		if _, err := r.core.fs.Stat(ctx, parent); err != nil {
			if !vfstypes.IsNotFound(err) {
				return err
			}
			if _, err := r.core.fs.Mkdir(ctx, parent); err != nil {
				return fmt.Errorf("core: create offline download directory %s: %w", parent, err)
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("core: create offline download directory %s: %w", filepath.Dir(target), err)
	}
	return nil
}

func (r *offlineRun) destinationRoot() string {
	if r.detail.DownloadMount == "" {
		return r.detail.DownloadTo
	}
	return r.core.offlineVirtualPath(r.detail.DownloadMount, r.detail.DownloadDir)
}

// offlineWriter is the destination writer of one transferred file.
type offlineWriter interface {
	Offset() int64
	WriteAt(p []byte, off int64) (int, error)
	// Close finalizes the transfer. For a namespace writer this flushes and
	// enqueues the upload, so a Close error means the destination was not
	// published.
	Close() error
	// Abort discards uncommitted bytes after a failed transfer so a partial
	// generation is never finalized or published.
	Abort() error
}

func (r *offlineRun) openWriter(ctx context.Context, target offlineFetchTarget) (offlineWriter, error) {
	if r.detail.DownloadMount != "" {
		return r.openNamespaceWriter(ctx, target)
	}
	return openLocalWriter(target.target)
}

// transferFile streams one provider file to its destination. An accelerated
// fetch goes straight to the provider download URL, because freshly staged
// content is often downloadable only through that session (a plain per-fid
// download URL is refused), and falls back to the staged read when the URL
// cannot be used. Every other fetch prefers the staged read - the VFS
// streaming reader is the driver's tuned path - and falls back to the URL.
func (r *offlineRun) transferFile(ctx context.Context, update task.UpdateFunc, target offlineFetchTarget, base int64) (int64, error) {
	if r.prefersURLTransfer() {
		written, err := r.transferFromURL(ctx, update, target, base)
		if err == nil {
			return written, nil
		}
		if staged, handled, stagingErr := r.transferFromStaging(ctx, update, target, base); handled {
			return staged, stagingErr
		}
		return written, err
	}
	if written, handled, err := r.transferFromStaging(ctx, update, target, base); handled {
		return written, err
	}
	return r.transferFromURL(ctx, update, target, base)
}

// prefersURLTransfer reports whether this fetch should use the provider
// download URL before the staged read.
func (r *offlineRun) prefersURLTransfer() bool {
	if !r.detail.Accelerated {
		return false
	}
	if _, ok := r.driver.(drive.OfflineFetcher); !ok {
		return false
	}
	return drive.HasCapability(r.driver, drive.CapabilityAcceleratedFetch)
}

// transferFromStaging copies the staged provider file through the VFS reader.
// It reports handled=false when the file is not readable that way yet (for
// example an accelerated transfer before the provider task finishes).
func (r *offlineRun) transferFromStaging(ctx context.Context, update task.UpdateFunc, target offlineFetchTarget, base int64) (int64, bool, error) {
	path, ok := r.stagedPath(target.file)
	if !ok {
		return 0, false, nil
	}
	streamer, ok := r.core.fs.(vfs.StreamReader)
	if !ok || !vfs.HasCapability(r.core.fs, vfs.CapabilityStreamRead) {
		return 0, false, nil
	}
	// The provider's directory listing can lag the completed task; refresh
	// before resolving the staged entry.
	r.core.fs.RefreshPath(pathpkg.Dir(path))
	reader, err := streamer.ReadStream(ctx, path)
	if err != nil {
		return 0, false, nil
	}
	defer reader.Close()
	writer, err := r.openWriter(ctx, target)
	if err != nil {
		return 0, true, err
	}
	offset := writer.Offset()
	if offset > 0 {
		if _, err := io.CopyN(io.Discard, reader, offset); err != nil {
			_ = writer.Abort()
			return 0, true, err
		}
	}
	written, copyErr := r.copyToWriter(ctx, update, reader, writer, offset, base)
	if copyErr != nil {
		_ = writer.Abort()
		return written, true, copyErr
	}
	if target.size > 0 && offset+written < target.size {
		// A reader that ends before the provider-reported size is a truncated
		// result, not a successful transfer. Never finalize it or let cleanup
		// delete the cloud source.
		_ = writer.Abort()
		return written, true, fmt.Errorf("core: staged transfer ended after %d of %d bytes", offset+written, target.size)
	}
	if closeErr := writer.Close(); closeErr != nil {
		_ = writer.Abort()
		return written, true, fmt.Errorf("core: finalize offline destination: %w", closeErr)
	}
	return written, true, nil
}

// stagedPath is the Core-view path of a staged result file.
func (r *offlineRun) stagedPath(file offlineResultFile) (string, bool) {
	if r.detail.ParseDir == "" || file.Name == "" {
		return "", false
	}
	return vfstypes.JoinVirtualPath(r.stagingRoot(), file.Name), true
}

// copyToWriter streams reader into the destination writer, reporting progress.
func (r *offlineRun) copyToWriter(ctx context.Context, update task.UpdateFunc, reader io.Reader, writer offlineWriter, offset, base int64) (int64, error) {
	buffer := make([]byte, offlineTransferChunk)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, writeErr := writer.WriteAt(buffer[:n], offset+written); writeErr != nil {
				return written, writeErr
			}
			written += int64(n)
			r.reportTransferProgress(update, base+written)
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

// transferFromURL streams one provider file from a signed download URL,
// refreshing it when it is missing or expired and resuming from the current
// offset. Only contiguously written bytes count as progress, so a failed
// attempt resumes from the last complete prefix instead of skipping holes.
func (r *offlineRun) transferFromURL(ctx context.Context, update task.UpdateFunc, target offlineFetchTarget, base int64) (int64, error) {
	writer, err := r.openWriter(ctx, target)
	if err != nil {
		return 0, err
	}
	offset := writer.Offset()
	size := target.size
	var lastErr error
	for attempt := 0; attempt < offlineURLRefreshes; attempt++ {
		if err := ctx.Err(); err != nil {
			_ = writer.Abort()
			return offset, err
		}
		downloadTarget, err := r.requestURL(ctx, target, offset)
		if err != nil {
			lastErr = err
			continue
		}
		result, err := r.streamURL(ctx, update, downloadTarget, offset, size, writer, base+offset)
		offset += result.written
		if result.size > 0 {
			size = result.size
		}
		if err != nil {
			lastErr = err
			continue
		}
		if result.done {
			if err := writer.Close(); err != nil {
				_ = writer.Abort()
				return offset, fmt.Errorf("core: finalize offline destination: %w", err)
			}
			return offset, nil
		}
		lastErr = fmt.Errorf("core: provider download URL ended before the file completed")
	}
	_ = writer.Abort()
	if lastErr == nil {
		lastErr = fmt.Errorf("core: download URL refresh exhausted")
	}
	return offset, lastErr
}

// requestURL asks the provider for a download URL. It prefers the accelerated
// session when the task negotiated one and degrades to the normal URL when the
// provider did not return a session (or the session has expired), so a missing
// session never fails the transfer.
func (r *offlineRun) requestURL(ctx context.Context, target offlineFetchTarget, offset int64) (drive.OfflineDownloadTarget, error) {
	fetcher, ok := r.driver.(drive.OfflineFetcher)
	if !ok {
		return drive.OfflineDownloadTarget{}, fmt.Errorf("%w: provider does not expose download URLs", ErrOfflineUnsupported)
	}
	files := []drive.OfflineFile{}
	if target.file.Name != "" || target.file.RemoteID != "" {
		files = append(files, drive.OfflineFile{Index: target.file.Index, Name: target.file.Name, Size: target.file.Size, RemoteID: target.file.RemoteID})
	}
	request := drive.OfflineFetchRequest{
		ID:          r.detail.ProviderID,
		Destination: r.detail.ParseDir,
		Files:       files,
		Accelerated: r.detail.Accelerated,
		Offset:      offset,
	}
	result, err := fetcher.OfflineDownloadURL(ctx, request)
	if err == nil || !request.Accelerated {
		return result, err
	}
	request.Accelerated = false
	return fetcher.OfflineDownloadURL(ctx, request)
}

// streamURL copies one file from a provider download URL with the production
// chunk plan.
func (r *offlineRun) streamURL(ctx context.Context, update task.UpdateFunc, target drive.OfflineDownloadTarget, start, size int64, writer offlineWriter, base int64) (offlineStreamResult, error) {
	fetch := offlineURLFetch{
		client: offlineHTTPClient,
		target: target,
		writer: writer,
		update: update,
		base:   base,
		plan:   r.transferPlan(),
	}
	return fetch.run(ctx, start, size)
}

// offlineChunkPlan is the bounded-range plan for one provider download URL.
type offlineChunkPlan struct {
	// chunk is the byte span of one Range request.
	chunk int64
	// workers is how many Range requests run at the same time.
	workers int
}

func defaultOfflineChunkPlan() offlineChunkPlan {
	return offlineChunkPlan{chunk: offlineFetchChunk, workers: offlineFetchWorkers}
}

// transferPlan is the chunk plan for this task: the caller's tuning when it set
// one, otherwise the defaults.
func (r *offlineRun) transferPlan() offlineChunkPlan {
	plan := defaultOfflineChunkPlan()
	if r.detail.TransferChunk > 0 {
		plan.chunk = r.detail.TransferChunk
	}
	if r.detail.TransferWorkers > 0 {
		plan.workers = r.detail.TransferWorkers
	}
	return plan
}

// offlineStreamResult reports one attempt at streaming a download URL.
type offlineStreamResult struct {
	// written is the number of contiguously written bytes from the requested
	// start; a caller resuming after a failure continues at start+written.
	written int64
	// size is the total file size when the provider disclosed one.
	size int64
	// done reports that the whole file is present at the destination.
	done bool
}

// offlineURLFetch copies one provider download URL into a destination writer.
// Every request carries a bounded Range, because a provider may serve an
// unbounded request far more slowly. The first response also decides whether
// the provider honours ranges at all: a provider that ignores them gets a
// single sequential stream instead of parallel chunks.
type offlineURLFetch struct {
	client *http.Client
	target drive.OfflineDownloadTarget
	writer offlineWriter
	update task.UpdateFunc
	// base counts bytes already transferred for the whole task, so progress
	// stays monotonic across the files of a multi-file fetch.
	base int64
	plan offlineChunkPlan
	// progress is created by run for the duration of one URL attempt.
	progress *offlineProgress
}

func (f offlineURLFetch) run(ctx context.Context, start, size int64) (offlineStreamResult, error) {
	f.progress = &offlineProgress{update: f.update, base: f.base}
	if f.target.Size > 0 {
		size = f.target.Size
	}
	if size > 0 && start >= size {
		return offlineStreamResult{size: size, done: true}, nil
	}
	if f.plan.chunk <= 0 {
		f.plan.chunk = offlineFetchChunk
	}
	if f.plan.workers <= 0 {
		f.plan.workers = offlineFetchWorkers
	}
	if size <= 0 {
		return f.runUnknownSize(ctx, start)
	}
	if start+f.plan.chunk >= size {
		// One bounded response covers everything that remains.
		return f.runSingleRange(ctx, start, size-start, size)
	}
	return f.runChunks(ctx, start, size)
}

// runUnknownSize discovers whether the provider honours a bounded Range and how
// large the file is, using a one-byte request. A provider that ignores ranges
// answers 200 with the whole file, which is then streamed as-is.
func (f offlineURLFetch) runUnknownSize(ctx context.Context, start int64) (offlineStreamResult, error) {
	response, err := f.openRange(ctx, start, 1)
	if err != nil {
		return offlineStreamResult{}, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusRequestedRangeNotSatisfiable:
		if err := validateRangeNotSatisfiable(response, start, 0); err != nil {
			return offlineStreamResult{}, err
		}
		return offlineStreamResult{done: true}, nil
	case http.StatusOK:
		written, err := f.copyBody(ctx, response.Body, start, start, 0)
		if err != nil {
			return offlineStreamResult{written: written}, err
		}
		return offlineStreamResult{written: written, done: true}, nil
	case http.StatusPartialContent:
		if err := validatePartialResponse(response, start, 0); err != nil {
			return offlineStreamResult{}, err
		}
		size := contentRangeTotal(response)
		if _, err := io.CopyN(io.Discard, response.Body, 1); err != nil && err != io.EOF {
			return offlineStreamResult{}, err
		}
		if size <= 0 {
			result, err := f.runSequentialRanges(ctx, start, 0)
			result.size = 0
			return result, err
		}
		if start >= size {
			return offlineStreamResult{size: size, done: true}, nil
		}
		if start+f.plan.chunk >= size {
			return f.runSingleRange(ctx, start, size-start, size)
		}
		return f.runChunks(ctx, start, size)
	default:
		return offlineStreamResult{}, fmt.Errorf("core: provider download URL returned status %d", response.StatusCode)
	}
}

// runSingleRange copies one bounded response that covers the file remainder.
func (f offlineURLFetch) runSingleRange(ctx context.Context, start, length, size int64) (offlineStreamResult, error) {
	response, err := f.openRange(ctx, start, length)
	if err != nil {
		return offlineStreamResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		// 416 is only success when the requested start really is at or past
		// the end of the file the provider reports. A provider that answers
		// 416 while bytes are still missing must not let the transfer claim
		// success (and let cleanup delete the cloud copy).
		if err := validateRangeNotSatisfiable(response, start, size); err != nil {
			return offlineStreamResult{size: size}, err
		}
		return offlineStreamResult{size: size, done: true}, nil
	}
	skip := int64(0)
	if response.StatusCode == http.StatusOK {
		// The provider ignored the Range header, so the body starts at zero.
		skip = start
	} else if response.StatusCode != http.StatusPartialContent {
		return offlineStreamResult{}, fmt.Errorf("core: provider download URL returned status %d", response.StatusCode)
	} else if err := validatePartialResponse(response, start, size); err != nil {
		return offlineStreamResult{size: size}, err
	}
	written, err := f.copyBody(ctx, response.Body, start, skip, size)
	if err != nil {
		return offlineStreamResult{written: written, size: size}, err
	}
	return offlineStreamResult{written: written, size: size, done: size <= 0 || start+written >= size}, nil
}

// runChunks copies the remaining span with several bounded Range requests in
// flight. Chunks are handed out in order and a chunk that fails stops the
// attempt; the reported progress only counts the contiguous prefix so the next
// attempt restarts at the first gap.
func (f offlineURLFetch) runChunks(ctx context.Context, start, size int64) (offlineStreamResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu         sync.Mutex
		firstErr   error
		next       = start
		contiguous = start
		completed  = map[int64]int64{}
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	// wholeFile lets a provider that ignores ranges take over the transfer on
	// one connection while the other workers stop.
	wholeFile := false
	worker := func() {
		for {
			mu.Lock()
			if firstErr != nil || wholeFile || next >= size {
				mu.Unlock()
				return
			}
			offset := next
			next += f.plan.chunk
			mu.Unlock()
			length := min(f.plan.chunk, size-offset)
			response, err := f.openRange(ctx, offset, length)
			if err != nil {
				fail(err)
				return
			}
			if response.StatusCode == http.StatusOK {
				// Ranges are ignored: this response is the whole file from
				// zero. Stream it here; the other workers stop.
				mu.Lock()
				wholeFile = true
				mu.Unlock()
				copied, copyErr := f.copyBody(ctx, response.Body, start, start, size)
				response.Body.Close()
				if copyErr != nil {
					fail(copyErr)
					return
				}
				mu.Lock()
				completed = map[int64]int64{}
				contiguous = start + copied
				mu.Unlock()
				return
			}
			if response.StatusCode != http.StatusPartialContent {
				response.Body.Close()
				fail(fmt.Errorf("core: provider download URL returned status %d", response.StatusCode))
				return
			}
			if validateErr := validatePartialResponse(response, offset, size); validateErr != nil {
				response.Body.Close()
				fail(validateErr)
				return
			}
			copied, copyErr := f.copyChunk(ctx, response.Body, offset, length, f.progress.add)
			response.Body.Close()
			if copyErr != nil {
				fail(copyErr)
				return
			}
			if copied != length {
				fail(fmt.Errorf("core: provider returned %d of %d bytes for range %d", copied, length, offset))
				return
			}
			mu.Lock()
			completed[offset] = offset + copied
			for {
				end, ok := completed[contiguous]
				if !ok {
					break
				}
				delete(completed, contiguous)
				contiguous = end
			}
			mu.Unlock()
		}
	}
	workers := f.plan.workers
	if workers > 1 {
		var wait sync.WaitGroup
		for range workers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				worker()
			}()
		}
		wait.Wait()
	} else {
		worker()
	}
	mu.Lock()
	defer mu.Unlock()
	writtenFromStart := contiguous - start
	if firstErr != nil {
		return offlineStreamResult{written: writtenFromStart, size: size}, firstErr
	}
	return offlineStreamResult{written: writtenFromStart, size: size, done: contiguous >= size}, nil
}

// runSequentialRanges copies a provider file whose total size is unknown but
// that does honour ranges, one bounded chunk at a time, until a short chunk.
func (f offlineURLFetch) runSequentialRanges(ctx context.Context, start, known int64) (offlineStreamResult, error) {
	offset := start
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return offlineStreamResult{written: written}, err
		}
		response, err := f.openRange(ctx, offset, f.plan.chunk)
		if err != nil {
			return offlineStreamResult{written: written}, err
		}
		if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			response.Body.Close()
			if err := validateRangeNotSatisfiable(response, offset, known); err != nil {
				return offlineStreamResult{written: written}, err
			}
			return offlineStreamResult{written: written, done: true}, nil
		}
		if response.StatusCode == http.StatusOK {
			// Ranges are ignored after all: the body is the whole file.
			copied, copyErr := f.copyBody(ctx, response.Body, start, start, known)
			response.Body.Close()
			if copyErr != nil {
				return offlineStreamResult{written: copied}, copyErr
			}
			return offlineStreamResult{written: copied, done: true}, nil
		}
		if response.StatusCode != http.StatusPartialContent {
			response.Body.Close()
			return offlineStreamResult{written: written}, fmt.Errorf("core: provider download URL returned status %d", response.StatusCode)
		}
		if err := validatePartialResponse(response, offset, known); err != nil {
			response.Body.Close()
			return offlineStreamResult{written: written}, err
		}
		copied, copyErr := f.copyChunk(ctx, response.Body, offset, f.plan.chunk, f.progress.add)
		response.Body.Close()
		if copyErr != nil {
			return offlineStreamResult{written: written}, copyErr
		}
		offset += copied
		if copied < f.plan.chunk {
			return offlineStreamResult{written: written, done: true}, nil
		}
	}
}

// openRange issues one bounded Range request for the provider URL.
func (f offlineURLFetch) openRange(ctx context.Context, offset, length int64) (*http.Response, error) {
	if length <= 0 {
		length = 1
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.target.URL, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range f.target.Headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	response, err := f.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 && response.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		response.Body.Close()
		return nil, fmt.Errorf("core: provider download URL returned status %d", response.StatusCode)
	}
	return response, nil
}

// copyBody streams a whole response body into the writer at start, skipping
// skip leading body bytes (a provider that ignored the Range sent the file from
// zero). A known size bounds the copy.
func (f offlineURLFetch) copyBody(ctx context.Context, body io.Reader, start, skip, size int64) (int64, error) {
	if skip > 0 {
		if _, err := io.CopyN(io.Discard, body, skip); err != nil {
			if err == io.EOF {
				return 0, nil
			}
			return 0, err
		}
	}
	return f.copyChunk(ctx, body, start, remaining(size, start), f.progress.add)
}

// copyChunk copies up to length bytes of body to the writer at offset,
// reporting each buffer as it lands.
func (f offlineURLFetch) copyChunk(ctx context.Context, body io.Reader, offset, length int64, report func(int64)) (int64, error) {
	buffer := make([]byte, offlineTransferChunk)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		if length > 0 && written >= length {
			return written, nil
		}
		want := buffer
		if length > 0 && length-written < int64(len(buffer)) {
			want = buffer[:length-written]
		}
		n, readErr := body.Read(want)
		if n > 0 {
			if _, writeErr := f.writer.WriteAt(want[:n], offset+written); writeErr != nil {
				return written, writeErr
			}
			written += int64(n)
			report(int64(n))
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

// offlineProgress publishes absolute transferred byte counts to the task. It is
// safe for the concurrent chunk workers.
type offlineProgress struct {
	update task.UpdateFunc
	base   int64

	mu    sync.Mutex
	total int64
}

// add reports delta more transferred bytes.
func (p *offlineProgress) add(delta int64) {
	if p == nil || p.update == nil {
		return
	}
	p.mu.Lock()
	p.total += delta
	done := p.base + p.total
	p.mu.Unlock()
	p.update(func(item *task.Task) {
		item.Progress.TransferBytesDone = done
	})
}

// contentRangeTotal reads the total size out of a Content-Range header,
// tolerating the `bytes start-end/total` and `bytes start-end/*` forms. A
// response without a usable total reports zero: a partial response's
// Content-Length only measures that chunk, so it is not a substitute.
func contentRangeTotal(response *http.Response) int64 {
	rng, ok := parseContentRangeHeader(response.Header.Get("Content-Range"))
	if !ok || !rng.hasTotal {
		return 0
	}
	return rng.total
}

// offlineContentRange is a parsed Content-Range header. hasStart distinguishes
// "bytes 0-..." from an unsatisfied-range form ("bytes */N").
type offlineContentRange struct {
	start    int64
	end      int64
	total    int64
	hasStart bool
	hasEnd   bool
	hasTotal bool
}

// parseContentRangeHeader accepts `bytes start-end/total`, `bytes start-end/*`,
// and `bytes * /total` (without the space) forms.
func parseContentRangeHeader(value string) (offlineContentRange, bool) {
	value = strings.TrimSpace(value)
	spec, ok := strings.CutPrefix(value, "bytes ")
	if !ok {
		return offlineContentRange{}, false
	}
	span, total, ok := strings.Cut(strings.TrimSpace(spec), "/")
	if !ok {
		return offlineContentRange{}, false
	}
	var rng offlineContentRange
	span = strings.TrimSpace(span)
	if span != "" && span != "*" {
		startText, endText, ok := strings.Cut(span, "-")
		if !ok {
			return offlineContentRange{}, false
		}
		start, startErr := strconv.ParseInt(strings.TrimSpace(startText), 10, 64)
		end, endErr := strconv.ParseInt(strings.TrimSpace(endText), 10, 64)
		if startErr != nil || endErr != nil {
			return offlineContentRange{}, false
		}
		rng.start, rng.end, rng.hasStart, rng.hasEnd = start, end, true, true
	}
	total = strings.TrimSpace(total)
	if total != "" && total != "*" {
		parsed, err := strconv.ParseInt(total, 10, 64)
		if err != nil {
			return offlineContentRange{}, false
		}
		rng.total, rng.hasTotal = parsed, true
	}
	return rng, true
}

// validatePartialResponse verifies that a 206 response starts at the offset we
// asked for and, when both sides disclose a total, that the totals agree.
// Providers that omit Content-Range are tolerated because the sequential copy
// and the contiguous-prefix bookkeeping still guard correctness.
func validatePartialResponse(response *http.Response, offset, expectedTotal int64) error {
	rng, ok := parseContentRangeHeader(response.Header.Get("Content-Range"))
	if !ok {
		return nil
	}
	if rng.hasStart && rng.start != offset {
		return fmt.Errorf("core: provider returned range starting at %d for request at %d", rng.start, offset)
	}
	if expectedTotal > 0 && rng.hasTotal && rng.total != expectedTotal {
		return fmt.Errorf("core: provider reports %d bytes but %d were expected", rng.total, expectedTotal)
	}
	return nil
}

// validateRangeNotSatisfiable decides whether a 416 means "the file ended
// exactly where we are" or "the provider rejected a range while bytes are still
// missing". Only the former may be reported as a complete file.
func validateRangeNotSatisfiable(response *http.Response, offset, expectedTotal int64) error {
	rng, ok := parseContentRangeHeader(response.Header.Get("Content-Range"))
	if expectedTotal > 0 {
		if ok && rng.hasTotal && rng.total != expectedTotal {
			return fmt.Errorf("core: provider reports %d bytes but %d were expected", rng.total, expectedTotal)
		}
		if offset >= expectedTotal {
			return nil
		}
		if ok && rng.hasTotal {
			return fmt.Errorf("core: provider rejected range at %d of %d bytes", offset, rng.total)
		}
		return fmt.Errorf("core: provider rejected range at %d without a usable size", offset)
	}
	if ok && rng.hasTotal && offset >= rng.total {
		return nil
	}
	return fmt.Errorf("core: provider rejected range at %d before the end of the file", offset)
}

// remaining is the number of bytes left from start when size is known, and 0
// (meaning "until EOF") when it is not.
func remaining(size, start int64) int64 {
	if size <= 0 || size <= start {
		return 0
	}
	return size - start
}

func (r *offlineRun) reportTransferProgress(update task.UpdateFunc, done int64) {
	if update == nil {
		return
	}
	update(func(item *task.Task) {
		item.Progress.TransferBytesDone = done
	})
}

// cleanupPhase removes the cloud copy this fetch staged, unless retention was
// requested. Only the fids recorded from this task's own provider observation
// are removed.
func (r *offlineRun) cleanupPhase(ctx context.Context, update task.UpdateFunc) error {
	if r.detail.KeepCloud {
		return nil
	}
	if len(r.detail.ResultFiles) == 0 {
		return nil
	}
	removed := 0
	for _, file := range r.detail.ResultFiles {
		if file.RemoteID == "" {
			continue
		}
		entry := drive.Entry{ID: file.RemoteID, Name: file.Name}
		if err := r.driver.Remove(ctx, entry); err != nil {
			if errors.Is(err, drive.ErrNotFound) {
				removed++
				continue
			}
			logging.L.Warnf("[OFFLINE] cleanup of staged fid %s failed: %v", file.RemoteID, err)
			continue
		}
		removed++
	}
	r.detail.CleanupDone = removed == len(r.detail.ResultFiles)
	return nil
}

// openLocalWriter opens a plain local destination for a fresh transfer. The
// file is truncated: a previous attempt may have left bytes beyond a hole (the
// parallel chunk workers land out of order), so its size is not a trustworthy
// resume prefix, and an unrelated file at the same path is not task-owned data.
// A restart re-fetches the file instead of silently skipping missing ranges.
func openLocalWriter(path string) (offlineWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &offlineLocalWriter{file: file, path: path}, nil
}

// offlineLocalWriter writes with pwrite so concurrent chunk workers can land in
// any order without sharing a file offset.
type offlineLocalWriter struct {
	file     *os.File
	path     string
	offset   int64
	finished bool
	aborted  bool
}

func (w *offlineLocalWriter) Offset() int64 { return w.offset }

func (w *offlineLocalWriter) WriteAt(p []byte, off int64) (int, error) {
	return w.file.WriteAt(p, off)
}

func (w *offlineLocalWriter) Close() error {
	if w.file == nil {
		return nil
	}
	// Flush before closing so a failure to persist the final bytes surfaces as
	// a transfer error instead of a silently successful file.
	syncErr := w.file.Sync()
	closeErr := w.file.Close()
	w.file = nil
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	w.finished = true
	return nil
}

// Abort closes and removes the file this writer created or truncated. It is
// only called after an unsuccessful transfer, including a Close that failed
// after the descriptor was already released.
func (w *offlineLocalWriter) Abort() error {
	if w.finished || w.aborted {
		return nil
	}
	w.aborted = true
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	if w.path == "" {
		return nil
	}
	err := os.Remove(w.path)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// openNamespaceWriter opens a destination inside a mounted namespace. Bytes are
// staged by the destination mount and uploaded on Flush, so encryption and the
// mount's upload path apply. Every transfer starts a fresh staging generation:
// a previous partial generation may contain holes, and a pending file is not a
// trustworthy resume prefix.
func (r *offlineRun) openNamespaceWriter(ctx context.Context, target offlineFetchTarget) (offlineWriter, error) {
	path := target.target
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if err := r.core.fs.Create(ctx, path); err != nil {
		return nil, err
	}
	return &offlineNamespaceWriter{ctx: ctx, fs: r.core.fs, path: path}, nil
}

type offlineNamespaceWriter struct {
	ctx      context.Context
	fs       vfs.Writer
	path     string
	offset   int64
	finished bool
	aborted  bool
}

func (w *offlineNamespaceWriter) Offset() int64 { return w.offset }

// WriteAt stages one chunk at an absolute offset. The VFS serializes writes per
// path, so concurrent chunk workers are safe here too.
func (w *offlineNamespaceWriter) WriteAt(p []byte, off int64) (int, error) {
	return w.fs.WriteAt(w.ctx, w.path, p, off)
}

func (w *offlineNamespaceWriter) Close() error {
	if w.finished {
		return nil
	}
	if err := w.fs.Flush(w.ctx, w.path); err != nil {
		return err
	}
	w.finished = true
	return nil
}

// Abort drops the pending staging generation without flushing it, so an
// incomplete transfer is not published and cannot be recovered as an upload.
func (w *offlineNamespaceWriter) Abort() error {
	if w.finished || w.aborted {
		return nil
	}
	w.aborted = true
	if discarder, ok := w.fs.(interface{ DiscardUpload(string) error }); ok {
		return discarder.DiscardUpload(w.path)
	}
	return nil
}
