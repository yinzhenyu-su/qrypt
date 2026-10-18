package core

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// Offline inspection tuning: how long an asynchronous provider parse is given
// before inspection reports a typed timeout.
const (
	offlineInspectAttempts = 6
	offlineInspectInterval = 2 * time.Second
)

// InspectOffline previews a source through the provider selected by the parse
// destination mount. It creates no task and persists nothing. Request-shape
// failures are classified so the transport returns a usage status.
func (c *Core) InspectOffline(ctx context.Context, req task.OfflineOperationRequest) (control.OfflinePreviewResult, error) {
	result, err := c.inspectOffline(ctx, req)
	return result, classifyOfflineRequestError(err)
}

func (c *Core) inspectOffline(ctx context.Context, req task.OfflineOperationRequest) (control.OfflinePreviewResult, error) {
	if c == nil || c.fs == nil {
		return control.OfflinePreviewResult{}, fmt.Errorf("core: closed")
	}
	normalized, err := NormalizeOfflineSource(req.Source)
	if err != nil {
		return control.OfflinePreviewResult{}, err
	}
	parseMount, parseDir, err := c.resolveParseDestinationSelector(req.ParseTo, req.Mount)
	if err != nil {
		return control.OfflinePreviewResult{}, err
	}
	mount, err := c.offlineMount(parseMount)
	if err != nil {
		return control.OfflinePreviewResult{}, err
	}
	driver, ok := c.offlineDriver(parseMount)
	if !ok {
		return control.OfflinePreviewResult{}, fmt.Errorf("%w: mount %q", ErrOfflineMountNotFound, parseMount)
	}
	if !drive.HasCapability(driver, drive.CapabilityOfflineDownload) {
		return control.OfflinePreviewResult{}, fmt.Errorf("%w: mount %q", ErrOfflineUnsupported, parseMount)
	}
	if !drive.HasCapability(driver, drive.CapabilityOfflineInspect) {
		return control.OfflinePreviewResult{}, drive.NewOfflineError(drive.OfflineErrInspectUnsupported, "", fmt.Sprintf("mount %q does not support offline inspection", parseMount))
	}
	if !drive.SupportsOfflineSourceKind(driver, normalized.Kind) {
		return control.OfflinePreviewResult{}, fmt.Errorf("%w: mount %q does not accept %s sources", ErrOfflineSourceUnsupported, parseMount, normalized.Kind)
	}

	result := control.OfflinePreviewResult{
		Source:      normalized.Source,
		SourceKind:  string(normalized.Kind),
		Mount:       parseMount,
		ParseDir:    parseDir,
		ParseUsable: true,
	}
	if mount.Encrypted {
		result.ParseUsable = false
		result.ParseError = fmt.Sprintf("mount %q is encrypted; provider-side staging bytes never pass through qrypt", parseMount)
	} else if err := c.checkParseDestination(ctx, parseMount, parseDir); err != nil {
		result.ParseUsable = false
		result.ParseError = err.Error()
	}

	inspector, ok := driver.(drive.OfflineInspector)
	if !ok {
		return control.OfflinePreviewResult{}, drive.NewOfflineError(drive.OfflineErrInspectUnsupported, "", fmt.Sprintf("mount %q does not support offline inspection", parseMount))
	}
	preview, err := c.inspectWithRetries(ctx, inspector, drive.OfflineDownloadRequest{
		SourceKind:  normalized.Kind,
		Source:      normalized.Source,
		Title:       req.Title,
		Destination: parseDir,
		Selection:   offlineDetailSelectionFromTask(req.Selection).toDrive(),
	})
	if err != nil {
		return control.OfflinePreviewResult{}, err
	}
	result.Title = preview.Title
	result.TotalSize = preview.TotalSize
	result.InstantSave = preview.InstantSave
	selected := offlineSelectIndexes(preview.Files, req.Selection)
	result.Files = make([]control.OfflinePreviewFile, 0, len(preview.Files))
	for i, file := range preview.Files {
		result.Files = append(result.Files, control.OfflinePreviewFile{
			Index:    i,
			Name:     file.Name,
			Size:     file.Size,
			RemoteID: file.RemoteID,
			Selected: selected[i],
		})
	}
	return result, nil
}

// checkParseDestination reports whether an explicit parse directory is usable
// without creating anything.
func (c *Core) checkParseDestination(ctx context.Context, mountName, dir string) error {
	if dir == "" {
		return nil
	}
	pathValue := c.offlineVirtualPath(mountName, dir)
	entry, err := c.fs.Stat(ctx, pathValue)
	if err != nil {
		return fmt.Errorf("parse destination %s: %w", pathValue, err)
	}
	if !entry.IsDir {
		return fmt.Errorf("parse destination %s is not a directory", pathValue)
	}
	return nil
}

func (c *Core) inspectWithRetries(ctx context.Context, inspector drive.OfflineInspector, req drive.OfflineDownloadRequest) (drive.OfflinePreview, error) {
	for attempt := 0; attempt < offlineInspectAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return drive.OfflinePreview{}, err
		}
		preview, err := inspector.InspectOfflineDownload(ctx, req)
		if err == nil {
			return preview, nil
		}
		if !errors.Is(err, drive.ErrOfflineInspectPending) {
			return drive.OfflinePreview{}, err
		}
		if err := sleepContext(ctx, offlineInspectInterval); err != nil {
			return drive.OfflinePreview{}, err
		}
	}
	return drive.OfflinePreview{}, drive.NewOfflineError(drive.OfflineErrWaitTimeout, "", "provider parse did not finish in time")
}

// offlineSelectIndexes applies a selection to the parsed files and returns, per
// file, whether it is selected.
func offlineSelectIndexes(files []drive.OfflineFile, selection task.OfflineSelection) []bool {
	selected := make([]bool, len(files))
	for i := range selected {
		selected[i] = true
	}
	mode := selection.Mode
	if mode == "" {
		mode = task.OfflineSelectionAll
	}
	switch mode {
	case task.OfflineSelectionAll:
		return selected
	case task.OfflineSelectionLargest:
		order := make([]int, len(files))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return files[order[a]].Size > files[order[b]].Size })
		for i := range selected {
			selected[i] = false
		}
		for i := 0; i < selection.Largest && i < len(order); i++ {
			selected[order[i]] = true
		}
		return selected
	case task.OfflineSelectionInclude:
		for i, file := range files {
			selected[i] = offlineMatches(file.Name, selection.Include, selection.Exclude)
		}
		return selected
	default:
		return selected
	}
}

func offlineMatches(name string, include, exclude []string) bool {
	if len(include) > 0 {
		matched := false
		for _, pattern := range include {
			if offlineGlobMatch(pattern, name) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, pattern := range exclude {
		if offlineGlobMatch(pattern, name) {
			return false
		}
	}
	return true
}

func offlineGlobMatch(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	if ok, err := path.Match(pattern, name); err == nil && ok {
		return true
	}
	// A pattern without a separator also matches the base name.
	if strings.Contains(pattern, "/") {
		return false
	}
	ok, err := path.Match(pattern, path.Base(name))
	return err == nil && ok
}

// WaitOfflineTask observes an existing offline task to a terminal state without
// mutating it. A zero timeout waits indefinitely; a supplied timeout that
// elapses first returns a typed timeout and leaves the task running.
func (c *Core) WaitOfflineTask(ctx context.Context, id string, timeout time.Duration) (task.Task, error) {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	interval := c.offlinePollEvery()
	if interval <= 0 {
		interval = defaultOfflinePollInterval
	}
	for {
		if err := ctx.Err(); err != nil {
			return task.Task{}, err
		}
		current, err := c.GetTask(ctx, id)
		if err != nil {
			return task.Task{}, err
		}
		if offlineTaskTerminal(current.State) {
			return current, nil
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return current, drive.NewOfflineError(drive.OfflineErrWaitTimeout, "", fmt.Sprintf("offline task %s did not finish within %s", id, timeout))
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
