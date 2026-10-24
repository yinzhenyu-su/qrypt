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
	"strings"
	"time"

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
	// Pin the local destination root once, before validation and directory
	// creation, and use that handle for every file. Re-opening the path per
	// file would follow a destination root that an attacker renamed and
	// replaced with a symlink between files.
	if r.detail.DownloadMount == "" {
		rootPath := localTargetRoot(targets, asDirectory)
		if err := os.MkdirAll(rootPath, 0o755); err != nil {
			return fmt.Errorf("core: create offline download directory %s: %w", rootPath, err)
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			return fmt.Errorf("core: open offline download directory %s: %w", rootPath, err)
		}
		r.localRoot, r.localRootPath = root, rootPath
		defer func() {
			r.localRoot, r.localRootPath = nil, ""
			_ = root.Close()
		}()
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
	// localRoot is the local destination root that confines this target's
	// later open. It is empty for a namespace destination.
	localRoot string
}

// fetchTargets resolves which provider files map to which destination paths.
// The destination is treated as a directory when more than one file is
// selected, when it names an existing directory, or when it ends in a path
// separator; each file is then placed at its staged relative path under it
// (or by its own name when the provider reports no path, or when a flat layout
// was requested). Otherwise a single file is written to the destination path
// itself.
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
	if !asDirectory {
		if len(files) != 1 {
			return nil, false, offlinePathInvalidError("destination %q names a single file but %d results were selected", r.detail.DownloadTo, len(files))
		}
		components, err := r.placementComponents(files[0])
		if err != nil {
			return nil, false, err
		}
		if len(components) > 1 {
			return nil, false, offlinePathInvalidError("destination %q names a single file but the staged path %q has directories", r.detail.DownloadTo, stagedPathOf(files[0]))
		}
		target := offlineFetchTarget{file: files[0], target: base, size: files[0].Size}
		if r.detail.DownloadMount == "" {
			target.localRoot = filepath.Dir(base)
		}
		return []offlineFetchTarget{target}, false, nil
	}
	targets := make([]offlineFetchTarget, 0, len(files))
	seen := make(map[string]int, len(files))
	for index, file := range files {
		components, err := r.placementComponents(file)
		if err != nil {
			return nil, false, err
		}
		var destination string
		target := offlineFetchTarget{file: file, size: file.Size}
		if r.detail.DownloadMount != "" {
			destination = joinNamespaceDestination(base, strings.Join(components, "/"))
		} else {
			destination = joinDestination(base, filepath.Join(components...))
			target.localRoot = base
		}
		target.target = destination
		if previous, ok := seen[destination]; ok {
			return nil, false, offlinePathInvalidError("results %d and %d resolve to the same destination %q", previous+1, index+1, destination)
		}
		seen[destination] = index
		targets = append(targets, target)
	}
	return targets, asDirectory, nil
}

// localTargetRoot is the single local directory that confines every target of
// this transfer.
func localTargetRoot(targets []offlineFetchTarget, asDirectory bool) string {
	if len(targets) == 0 {
		return ""
	}
	if !asDirectory {
		return filepath.Dir(targets[0].target)
	}
	return targets[0].localRoot
}

// placementComponents returns the destination path components for one result
// file: the validated staged relative path, or the validated file name when the
// provider reports no path or a flat layout was requested.
func (r *offlineRun) placementComponents(file offlineResultFile) ([]string, error) {
	if !r.detail.Flat && file.Path != "" {
		return sanitizeOfflineRelPath(file.Path)
	}
	name := file.Name
	if name == "" {
		name = offlineResultItemID(file.Index, file)
	}
	component, err := sanitizeOfflineComponent(name)
	if err != nil {
		return nil, err
	}
	return []string{component}, nil
}

func stagedPathOf(file offlineResultFile) string {
	if file.Path != "" {
		return file.Path
	}
	return file.Name
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

// joinDestination joins a local directory destination and a relative path with
// native path rules, so a relative destination such as "./downloads/" stays
// relative and a Windows path keeps its own separator.
func joinDestination(destination, name string) string {
	return filepath.Join(destination, name)
}

// joinNamespaceDestination joins a virtual destination and a relative path with
// namespace rules, independent of the host OS.
func joinNamespaceDestination(destination, name string) string {
	return vfstypes.JoinVirtualPath(vfstypes.CleanVirtualPath(destination), name)
}

// ensureDownloadDirectory creates the destination directories the transfer
// writes into. A missing destination directory is created automatically, unlike
// a missing parse destination. Each file's parent chain is created and checked
// for symbolic-link escapes so a hostile staged path cannot leave the root.
func (r *offlineRun) ensureDownloadDirectory(ctx context.Context, targets []offlineFetchTarget, asDirectory bool) error {
	if !asDirectory {
		// A single file target: its parent is the user-named destination, so
		// only the final component needs confinement and a symlink check.
		if r.detail.DownloadMount == "" {
			return preflightLocalTarget(r.localRoot, r.localRootPath, targets[0].target)
		}
		return r.ensureParentDirectory(ctx, targets[0].target)
	}
	if r.detail.DownloadMount != "" {
		base := r.destinationRoot()
		if _, err := r.core.fs.Stat(ctx, base); err != nil {
			if !vfstypes.IsNotFound(err) {
				return err
			}
			if _, err := r.core.fs.Mkdir(ctx, base); err != nil {
				return fmt.Errorf("core: create offline download directory %s: %w", base, err)
			}
		}
		for _, target := range targets {
			if err := r.ensureNamespaceDirChain(ctx, base, target.target); err != nil {
				return err
			}
		}
		return nil
	}
	// Inspect every target before creating anything, so a hostile later target
	// cannot be discovered after an earlier file was written.
	for _, target := range targets {
		if err := preflightLocalTarget(r.localRoot, r.localRootPath, target.target); err != nil {
			return err
		}
	}
	for _, target := range targets {
		if err := ensureLocalDirChain(r.localRoot, r.localRootPath, filepath.Dir(target.target)); err != nil {
			return err
		}
	}
	// Re-check after directory creation, then ask the actual destination
	// filesystem whether any two final targets alias. A second preflight sees a
	// case/normalization alias that turned one target into another's parent.
	for _, target := range targets {
		if err := preflightLocalTarget(r.localRoot, r.localRootPath, target.target); err != nil {
			return err
		}
	}
	return r.validateLocalAliases(targets)
}

// validateLocalAliases mirrors every target inside a fresh, session-owned
// scratch directory under the pinned root. Creating each candidate with
// O_CREATE|O_EXCL lets the actual filesystem reveal aliases the name cannot
// show: ASCII case, Unicode canonical equivalence, and any other mapping the
// destination volume applies. Nothing is written at a real target path and the
// scratch tree is removed through the same pinned root.
func (r *offlineRun) validateLocalAliases(targets []offlineFetchTarget) error {
	if r.localRoot == nil {
		return nil
	}
	scratch, err := r.createLocalScratch()
	if err != nil {
		return err
	}
	defer func() { _ = r.localRoot.RemoveAll(scratch) }()
	for index, target := range targets {
		rel, relErr := filepath.Rel(r.localRootPath, target.target)
		if relErr != nil {
			return offlinePathInvalidError("destination %q is not under %q", target.target, r.localRootPath)
		}
		scratchRel := filepath.Join(scratch, rel)
		if err := ensureLocalDirChain(r.localRoot, r.localRootPath, filepath.Join(r.localRootPath, filepath.Dir(scratchRel))); err != nil {
			return err
		}
		file, openErr := r.localRoot.OpenFile(scratchRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if openErr != nil {
			if errors.Is(openErr, os.ErrExist) {
				return offlinePathInvalidError("result %d destination %q aliases another result", index+1, target.target)
			}
			if info, infoErr := r.localRoot.Lstat(scratchRel); infoErr == nil && info.IsDir() {
				return offlinePathInvalidError("destination %q is a directory", target.target)
			}
			return openErr
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

// createLocalScratch makes a uniquely named empty directory inside the pinned
// root, retrying if a target or another transfer already owns the name.
func (r *offlineRun) createLocalScratch() (string, error) {
	for range 10 {
		name := fmt.Sprintf(".qrypt-offline-scratch-%d-%d", os.Getpid(), time.Now().UnixNano())
		err := r.localRoot.Mkdir(name, 0o700)
		if err == nil {
			return name, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("core: could not reserve an offline scratch directory")
}

// preflightLocalTarget verifies that no existing component of target under
// root is a symbolic link and that the final component is not a directory. It
// checks the whole target list before a transfer creates any file.
func preflightLocalTarget(root *os.Root, rootPath, target string) error {
	if root == nil {
		return nil
	}
	rel, err := filepath.Rel(rootPath, target)
	if err != nil {
		return offlinePathInvalidError("destination %q is not under %q", target, rootPath)
	}
	separator := string(filepath.Separator)
	if rel == ".." || strings.HasPrefix(rel, ".."+separator) {
		return offlinePathInvalidError("destination %q is outside %q", target, rootPath)
	}
	if rel == "." {
		return offlinePathInvalidError("destination %q names a directory", target)
	}
	parts := strings.Split(rel, separator)
	currentRel := ""
	for index, part := range parts {
		currentRel = filepath.Join(currentRel, part)
		info, err := root.Lstat(currentRel)
		switch {
		case os.IsNotExist(err):
			continue
		case err != nil:
			return err
		case info.Mode()&os.ModeSymlink != 0:
			return offlinePathInvalidError("destination %q resolves through a symbolic link at %q", target, filepath.Join(rootPath, currentRel))
		case index == len(parts)-1:
			if info.IsDir() {
				return offlinePathInvalidError("destination %q is a directory", target)
			}
		case !info.IsDir():
			return offlinePathInvalidError("destination %q is not a directory", filepath.Join(rootPath, currentRel))
		}
	}
	return nil
}

// ensureLocalDirChain creates the missing directory levels from root down to dir
// one level at a time through the pinned root, refusing an existing symlinked
// or non-directory ancestor so a staged path cannot escape it.
func ensureLocalDirChain(root *os.Root, rootPath, dir string) error {
	if root == nil {
		return nil
	}
	rel, err := filepath.Rel(rootPath, dir)
	if err != nil {
		return offlinePathInvalidError("destination directory %q is not under %q", dir, rootPath)
	}
	if rel == "." {
		return nil
	}
	separator := string(filepath.Separator)
	if rel == ".." || strings.HasPrefix(rel, ".."+separator) {
		return offlinePathInvalidError("destination directory %q is outside %q", dir, rootPath)
	}
	currentRel := ""
	for _, part := range strings.Split(rel, separator) {
		currentRel = filepath.Join(currentRel, part)
		info, err := root.Lstat(currentRel)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return offlinePathInvalidError("destination %q resolves through a symbolic link at %q", dir, filepath.Join(rootPath, currentRel))
			}
			if !info.IsDir() {
				return offlinePathInvalidError("destination %q is not a directory", filepath.Join(rootPath, currentRel))
			}
		case os.IsNotExist(err):
			if mkErr := root.Mkdir(currentRel, 0o755); mkErr != nil && !os.IsExist(mkErr) {
				return fmt.Errorf("core: create offline download directory %s: %w", filepath.Join(rootPath, currentRel), mkErr)
			}
		default:
			return err
		}
	}
	return nil
}

// ensureNamespaceDirChain creates the missing virtual directory levels from base
// down to target's parent inside the Core's own namespace.
func (r *offlineRun) ensureNamespaceDirChain(ctx context.Context, base, target string) error {
	base = trimTrailingSlash(vfstypes.CleanVirtualPath(base))
	parent := pathpkg.Dir(vfstypes.CleanVirtualPath(target))
	if parent == base || parent == "." {
		return nil
	}
	if base != "" && base != "/" && !strings.HasPrefix(parent, base+"/") {
		return offlinePathInvalidError("destination directory %q is outside %q", parent, base)
	}
	rest := strings.TrimPrefix(parent, base)
	for _, part := range strings.Split(rest, "/") {
		if part == "" {
			continue
		}
		if base == "" {
			base = "/"
		}
		base = vfstypes.JoinVirtualPath(base, part)
		if _, err := r.core.fs.Stat(ctx, base); err != nil {
			if !vfstypes.IsNotFound(err) {
				return err
			}
			if _, err := r.core.fs.Mkdir(ctx, base); err != nil {
				return fmt.Errorf("core: create offline download directory %s: %w", base, err)
			}
		}
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

func (r *offlineRun) openWriter(ctx context.Context, target offlineFetchTarget) (offlineWriter, error) {
	if r.detail.DownloadMount != "" {
		return r.openNamespaceWriter(ctx, target)
	}
	if r.localRoot == nil {
		return nil, fmt.Errorf("core: offline download destination root is not pinned")
	}
	rel, err := filepath.Rel(r.localRootPath, target.target)
	if err != nil {
		return nil, offlinePathInvalidError("destination %q is not under %q", target.target, r.localRootPath)
	}
	return openLocalWriterIn(r.localRoot, rel, target.target, false)
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
	rel := stagedPathOf(file)
	if r.detail.ParseDir == "" || rel == "" {
		return "", false
	}
	return vfstypes.JoinVirtualPath(r.stagingRoot(), rel), true
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
	return streamDownloadTarget(ctx, target, start, size, writer, base, r.transferPlan(), update)
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
// are removed, plus the resource folder the provider created for them once it
// holds nothing else.
func (r *offlineRun) cleanupPhase(ctx context.Context, update task.UpdateFunc) error {
	if r.detail.KeepCloud {
		return nil
	}
	if len(r.detail.ResultFiles) == 0 {
		return nil
	}
	removedFIDs := make(map[string]bool, len(r.detail.ResultFiles))
	removed := 0
	for _, file := range r.detail.ResultFiles {
		if file.RemoteID == "" {
			continue
		}
		entry := drive.Entry{ID: file.RemoteID, Name: file.Name}
		if err := r.driver.Remove(ctx, entry); err != nil {
			if errors.Is(err, drive.ErrNotFound) {
				removed++
				removedFIDs[file.RemoteID] = true
				continue
			}
			logging.L.Warnf("[OFFLINE] cleanup of staged fid %s failed: %v", file.RemoteID, err)
			continue
		}
		removed++
		removedFIDs[file.RemoteID] = true
	}
	r.detail.CleanupDone = removed == len(r.detail.ResultFiles)
	if r.detail.CleanupDone {
		r.cleanupStagedRootFolder(ctx, removedFIDs)
	}
	return nil
}

// cleanupStagedRootFolder removes the resource folder the provider created for
// this task once every file under it is one this cleanup removed. A magnet makes
// the provider create that folder, so removing only its files would leave an
// empty shell behind. The parse destination itself is never removed, and a folder
// that still holds an unrelated entry is left untouched.
func (r *offlineRun) cleanupStagedRootFolder(ctx context.Context, removed map[string]bool) {
	rootID := r.detail.ProviderRootID
	folder := r.stagedRootFolder()
	if rootID == "" || folder == "" {
		return
	}
	for attempt := 0; attempt < offlineVerifyAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepContext(ctx, offlineVerifyInterval); err != nil {
				return
			}
			r.core.fs.RefreshPath(folder)
		}
		owned, err := r.stagedTreeOwned(ctx, folder, removed, 0)
		if err != nil || !owned {
			return
		}
		// The provider's directory delete is recursive, so one call removes the
		// folder and any now-empty subdirectories under it.
		if err := r.driver.Remove(ctx, drive.Entry{ID: rootID, Name: pathpkg.Base(folder)}); err != nil {
			if errors.Is(err, drive.ErrNotFound) {
				return
			}
			logging.L.Warnf("[OFFLINE] cleanup of staged folder %s failed: %v", folder, err)
			continue
		}
		return
	}
}

// stagedTreeOwned reports whether every file under folder is one this cleanup
// removed. Directories are not evidence about ownership, so they are walked.
func (r *offlineRun) stagedTreeOwned(ctx context.Context, folder string, removed map[string]bool, depth int) (bool, error) {
	if depth > offlineMaxPathDepth {
		return false, nil
	}
	entries, err := r.core.fs.List(ctx, folder)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir {
			owned, err := r.stagedTreeOwned(ctx, vfstypes.JoinVirtualPath(folder, entry.Name), removed, depth+1)
			if err != nil || !owned {
				return false, err
			}
			continue
		}
		if !removed[entry.ID] {
			return false, nil
		}
	}
	return true, nil
}

// stagedRootFolder returns the Core-view path of the single top-level folder the
// staged result files share, or "" when they have none. Files staged directly in
// the parse destination, or under several top-level entries, yield "" so the
// parse destination is never removed.
func (r *offlineRun) stagedRootFolder() string {
	root := ""
	for _, file := range r.detail.ResultFiles {
		rel := strings.Trim(strings.ReplaceAll(stagedPathOf(file), "\\", "/"), "/")
		index := strings.IndexByte(rel, '/')
		if index <= 0 {
			return ""
		}
		first := rel[:index]
		if root == "" {
			root = first
			continue
		}
		if root != first {
			return ""
		}
	}
	if root == "" {
		return ""
	}
	return vfstypes.JoinVirtualPath(r.stagingRoot(), root)
}

// openLocalWriter opens a plain local destination for a fresh transfer. The
// file is truncated: a previous attempt may have left bytes beyond a hole (the
// parallel chunk workers land out of order), so its size is not a trustworthy
// resume prefix, and an unrelated file at the same path is not task-owned data.
// A restart re-fetches the file instead of silently skipping missing ranges.
func openLocalWriter(path string) (offlineWriter, error) {
	rootPath := filepath.Dir(path)
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(rootPath, path)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	writer, err := openLocalWriterIn(root, rel, path, true)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return writer, nil
}

// openLocalWriterIn opens a local destination through an already-pinned root.
// The writer never reopens or removes by pathname: Abort goes through the same
// root handle, so replacing the destination root with a symlink cannot redirect
// a write or a deletion outside it.
func openLocalWriterIn(root *os.Root, rel, path string, ownsRoot bool) (offlineWriter, error) {
	separator := string(filepath.Separator)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+separator) {
		return nil, offlinePathInvalidError("destination %q is outside the pinned root", path)
	}
	file, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &offlineLocalWriter{file: file, root: root, rel: rel, ownsRoot: ownsRoot}, nil
}

// offlineLocalWriter writes with pwrite so concurrent chunk workers can land in
// any order without sharing a file offset. root is borrowed for a shared
// transfer and owned only by the standalone openLocalWriter helper.
type offlineLocalWriter struct {
	file     *os.File
	root     *os.Root
	rel      string
	ownsRoot bool
	offset   int64
	finished bool
	aborted  bool
}

func (w *offlineLocalWriter) closeRoot() {
	if w.ownsRoot && w.root != nil {
		_ = w.root.Close()
		w.root = nil
	}
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
	// a transfer error instead of a silently successful file. Keep the pinned
	// root open on failure so Abort can remove the partial file through it.
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
	w.closeRoot()
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
	var err error
	if w.root != nil && w.rel != "" {
		err = w.root.Remove(w.rel)
	}
	w.closeRoot()
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
