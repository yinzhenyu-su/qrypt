package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Core-level offline request problems. They classify a request that was
// rejected before any provider task existed; the CLI maps the input ones to a
// usage exit code.
var (
	// ErrOfflineMountRequired means no parse destination mount could be
	// selected (neither --parse-to nor --mount was given).
	ErrOfflineMountRequired = errors.New("core: offline parse destination mount is required")
	// ErrOfflineMountConflict means two selectors named different mounts.
	ErrOfflineMountConflict = errors.New("core: offline mount selection conflict")
	// ErrOfflineMountNotFound means the named mount is not configured.
	ErrOfflineMountNotFound = errors.New("core: offline mount not found")
	// ErrOfflineMountUnavailable means the mount failed to initialize.
	ErrOfflineMountUnavailable = errors.New("core: offline mount is unavailable")
	// ErrOfflineEncryptedParseDestination means the parse destination mount
	// applies qrypt encryption, which provider-side bytes never pass through.
	ErrOfflineEncryptedParseDestination = errors.New("core: encrypted parse destination is not supported")
	// ErrOfflineParseDestinationMissing means the parse directory does not
	// exist and creation was not requested.
	ErrOfflineParseDestinationMissing = errors.New("core: offline parse destination does not exist")
	// ErrOfflineSameDestination means the parse and download destinations
	// resolve to the same mount and directory.
	ErrOfflineSameDestination = errors.New("core: offline parse and download destinations are identical")
	// ErrOfflineUnsupported means the mount's provider does not declare the
	// requested offline capability.
	ErrOfflineUnsupported = errors.New("core: offline download is unsupported for this mount")
	// ErrOfflineSourceUnsupported means the provider does not accept the
	// normalized source kind.
	ErrOfflineSourceUnsupported = errors.New("core: unsupported offline source kind")
	// ErrOfflineNotRunning means no Core owns the task store for the request.
	ErrOfflineNotRunning = errors.New("core: no running Core owns the offline task store")
)

// offlineSpec is a fully resolved, provider-neutral offline request. Every
// field that a provider or the runner needs is resolved before a task is
// created; a resolution failure creates no task.
type offlineSpec struct {
	fetch      bool
	sourceKind drive.OfflineSourceKind
	source     string
	identity   string
	title      string

	parseMount string
	parseDir   string // mount-relative; "" means the provider default
	// parseRemoteID is the provider's identity for an explicit parse
	// directory, resolved once by Stat/Mkdir so the runner does not have to
	// re-resolve it through an eventually consistent listing.
	parseRemoteID string

	selection     task.OfflineSelection
	mkdir         bool
	newTask       bool
	downloadTo    string
	downloadDir   string // mount-relative when downloadMount != ""
	downloadMount string
	keepCloud     bool
	accelerated   bool
	// transferWorkers and transferChunk tune the URL transfer: how many
	// bounded Range requests run at once and how many bytes each covers. Zero
	// means the Core defaults.
	transferWorkers int
	transferChunk   int64
	warnings        []string

	driver    drive.Driver
	encrypted bool
}

// createOfflineDownloadTask creates a cloud-side offline task. See
// createOfflineFetchTask for the fetch variant.
func (c *Core) createOfflineDownloadTask(ctx context.Context, req task.Request) (task.Task, error) {
	return c.createOfflineTask(ctx, req, req.Type == task.TypeOfflineFetch)
}

// createOfflineFetchTask creates an offline fetch: a cloud staging phase
// followed by a transfer phase into a final destination.
func (c *Core) createOfflineFetchTask(ctx context.Context, req task.Request) (task.Task, error) {
	return c.createOfflineTask(ctx, req, true)
}

func (c *Core) createOfflineTask(ctx context.Context, req task.Request, fetch bool) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	if req.Offline == nil {
		return task.Task{}, fmt.Errorf("%w: offline inputs are required", task.ErrInvalidOperation)
	}
	if err := req.Offline.Validate(); err != nil {
		return task.Task{}, err
	}
	spec, err := c.resolveOfflineSpec(ctx, *req.Offline, fetch)
	if err != nil {
		return task.Task{}, err
	}

	detail := spec.detail()
	manager, err := c.taskManager()
	if err != nil {
		return task.Task{}, err
	}
	activeFingerprint := offlineFingerprint(detail)

	now := offlineNow()
	item := task.Task{
		ID:           newOfflineTaskID(spec.fetch),
		Type:         offlineTaskType(spec.fetch),
		State:        task.StateQueued,
		Scope:        task.ScopeForType(offlineTaskType(spec.fetch)),
		Visibility:   task.VisibilityForType(offlineTaskType(spec.fetch)),
		Mount:        spec.parseMount,
		Name:         spec.title,
		CreatedAt:    now,
		UpdatedAt:    now,
		Capabilities: taskCreationCapabilities(offlineTaskType(spec.fetch)),
		Progress: task.Progress{
			Phase:             offlinePhaseStaging.display(),
			StagingBytesTotal: 0,
		},
	}
	detail.apply(&item)
	if len(spec.warnings) > 0 {
		item.Detail["warnings"] = append([]string(nil), spec.warnings...)
	}
	applyTaskRequestMetadata(&item, req)
	if item.OperationFingerprint == "" {
		// A direct offline Request (not built through CreateOperation) still
		// needs the operation-wide fingerprint: explicit keys (including keys
		// bound later to an active task) compare it to detect different
		// content such as a different final destination or cleanup policy.
		operation := task.OperationRequest{
			Operation: task.OperationDownload,
			Scope:     req.Scope,
			Items:     req.Items,
			Options:   req.Options,
			Detail:    req.Detail,
			Offline:   req.Offline,
		}
		if fingerprint, fingerprintErr := operationFingerprint(operation); fingerprintErr == nil {
			item.OperationFingerprint = fingerprint
		} else {
			item.OperationFingerprint = activeFingerprint
		}
	}
	operationFingerprint := item.OperationFingerprint

	// Active de-duplication and the manager's keyed idempotency both decide
	// "is this the same submission?". Serialize every lookup with the submit
	// so two simultaneous requests cannot both miss the other and create two
	// provider tasks.
	c.offlineCreateMu.Lock()
	defer c.offlineCreateMu.Unlock()

	if req.IdempotencyKey != "" {
		// The explicit key contract comes first: a bound key with the same
		// content returns that task (even terminal), and a bound key with
		// different content conflicts before active de-duplication could
		// return a task that ignores the key.
		keyed, found, keyErr := manager.FindIdempotent(item)
		if keyErr != nil {
			return task.Task{}, keyErr
		}
		if found {
			return keyed, nil
		}
	}

	if !spec.newTask {
		if existing, ok := c.findActiveOfflineTask(ctx, activeFingerprint); ok {
			// Preserve the spec's active de-duplication for keyed requests
			// too: the same source + parse destination + selection reuses the
			// active task. Bind every new explicit key to the same task so a
			// later same-key/different-content submission conflicts and a
			// restart can replay the key, without replacing the task's
			// primary key.
			if req.IdempotencyKey != "" && !task.HasIdempotencyKey(existing, req.IdempotencyKey) {
				bound, updated := manager.UpdateTask(existing.ID, func(current *task.Task) {
					task.BindIdempotencyKeyFingerprint(current, req.IdempotencyKey, operationFingerprint)
				})
				if updated {
					return bound, nil
				}
			}
			return existing, nil
		}
	}

	run := func(runCtx context.Context, update task.UpdateFunc) error {
		return c.runOfflineTask(runCtx, update, item.ID, spec.fetch)
	}
	return manager.SubmitIdempotent(ctx, item, run)
}

func offlineTaskType(fetch bool) task.Type {
	if fetch {
		return task.TypeOfflineFetch
	}
	return task.TypeOfflineDownload
}

// resolveOfflineSpec resolves every provider-neutral input to a concrete mount,
// provider, and destination, rejecting anything the request cannot satisfy
// before a task exists.
func (c *Core) resolveOfflineSpec(ctx context.Context, req task.OfflineOperationRequest, fetch bool) (offlineSpec, error) {
	normalized, err := NormalizeOfflineSource(req.Source)
	if err != nil {
		return offlineSpec{}, err
	}

	parseMount, parseDir, err := c.resolveParseDestinationSelector(req.ParseTo, req.Mount)
	if err != nil {
		return offlineSpec{}, err
	}
	mount, err := c.offlineMount(parseMount)
	if err != nil {
		return offlineSpec{}, err
	}
	driver, ok := c.offlineDriver(parseMount)
	if !ok {
		return offlineSpec{}, fmt.Errorf("%w: mount %q", ErrOfflineMountNotFound, parseMount)
	}
	if !drive.HasCapability(driver, drive.CapabilityOfflineDownload) {
		return offlineSpec{}, fmt.Errorf("%w: mount %q", ErrOfflineUnsupported, parseMount)
	}
	if !drive.SupportsOfflineSourceKind(driver, normalized.Kind) {
		return offlineSpec{}, fmt.Errorf("%w: mount %q does not accept %s sources", ErrOfflineSourceUnsupported, parseMount, normalized.Kind)
	}
	if mount.Encrypted {
		return offlineSpec{}, fmt.Errorf("%w: mount %q", ErrOfflineEncryptedParseDestination, parseMount)
	}
	parseRemoteID, err := c.ensureParseDestination(ctx, parseMount, parseDir, req.Mkdir)
	if err != nil {
		return offlineSpec{}, err
	}

	selection := req.Selection
	if err := c.checkOfflineSelection(driver, selection); err != nil {
		return offlineSpec{}, err
	}

	spec := offlineSpec{
		fetch:         fetch,
		sourceKind:    normalized.Kind,
		source:        normalized.Source,
		identity:      normalized.Identity,
		title:         strings.TrimSpace(req.Title),
		parseMount:    parseMount,
		parseDir:      parseDir,
		parseRemoteID: parseRemoteID,
		selection:     selection,
		mkdir:         req.Mkdir,
		newTask:       req.New,
		keepCloud:     req.KeepCloud,
		driver:        driver,
		encrypted:     mount.Encrypted,
	}
	if fetch {
		if strings.TrimSpace(req.DownloadTo) == "" {
			return offlineSpec{}, fmt.Errorf("%w: offline fetch requires a download destination", task.ErrInvalidOperation)
		}
		downloadMount, downloadDir, warnings, err := c.resolveDownloadDestination(spec, req.DownloadTo)
		if err != nil {
			return offlineSpec{}, err
		}
		spec.transferWorkers = req.Workers
		spec.transferChunk = req.ChunkSize
		spec.downloadTo = req.DownloadTo
		spec.downloadMount = downloadMount
		spec.downloadDir = downloadDir
		spec.warnings = warnings
		// A provider without accelerated fetch still fetches through its
		// normal download URL; the flag only enables the accelerated attempt.
		spec.accelerated = drive.HasCapability(driver, drive.CapabilityAcceleratedFetch)
	}
	return spec, nil
}

// resolveParseDestinationSelector splits the caller's selectors into a mount
// name and a mount-relative directory. An empty ParseTo with an explicit Mount
// leaves the directory empty (the provider default). Conflicting selectors are
// rejected.
func (c *Core) resolveParseDestinationSelector(parseTo, mountName string) (string, string, error) {
	parseTo = strings.TrimSpace(parseTo)
	mountName = vfstypes.CleanMountName(mountName)
	if parseTo == "" {
		if mountName == "" {
			return "", "", fmt.Errorf("%w: pass --parse-to or --mount", ErrOfflineMountRequired)
		}
		return mountName, "", nil
	}
	mount, dir, err := c.offlineSplitMountPath(parseTo)
	if err != nil {
		return "", "", err
	}
	if mountName != "" && mountName != mount {
		return "", "", fmt.Errorf("%w: --mount %q with parse destination on mount %q", ErrOfflineMountConflict, mountName, mount)
	}
	return mount, dir, nil
}

// offlineSplitMountPath splits a mount-qualified virtual path into its mount
// name and mount-relative directory. It respects whether the Core was opened
// as a namespace (/MOUNT/...) or as a single mount (/...).
func (c *Core) offlineSplitMountPath(virtual string) (mount, dir string, err error) {
	if !strings.HasPrefix(virtual, "/") {
		return "", "", fmt.Errorf("%w: parse destination %q must be an absolute path", task.ErrInvalidOperation, virtual)
	}
	cleaned := vfstypes.CleanVirtualPath(virtual)
	if !c.offlineIsNamespace() {
		name, ok := c.offlineSingleMountName()
		if !ok {
			return "", "", fmt.Errorf("%w: no mount is available", ErrOfflineMountNotFound)
		}
		return name, cleaned, nil
	}
	trimmed := strings.TrimPrefix(cleaned, "/")
	if trimmed == "" {
		return "", "", fmt.Errorf("%w: parse destination must name a mount", ErrOfflineMountRequired)
	}
	mount, rest, found := strings.Cut(trimmed, "/")
	if !found {
		return mount, "/", nil
	}
	return mount, vfstypes.CleanVirtualPath("/" + rest), nil
}

func (c *Core) offlineIsNamespace() bool {
	_, ok := c.fs.(*vfs.Namespace)
	return ok
}

func (c *Core) offlineSingleMountName() (string, bool) {
	mounts := c.offlineMounts()
	for _, mount := range mounts {
		if mount.State == "failed" {
			continue
		}
		return mount.Name, true
	}
	return "", false
}

func (c *Core) offlineMounts() []vfs.MountInfo {
	reporter, ok := c.fs.(vfs.MountReporter)
	if !ok {
		return nil
	}
	return reporter.Mounts()
}

func (c *Core) offlineMount(name string) (vfs.MountInfo, error) {
	cleaned := vfstypes.CleanMountName(name)
	for _, mount := range c.offlineMounts() {
		if mount.Name != cleaned {
			continue
		}
		if mount.State == "failed" {
			return vfs.MountInfo{}, fmt.Errorf("%w: mount %q: %s", ErrOfflineMountUnavailable, cleaned, mount.Error)
		}
		return mount, nil
	}
	return vfs.MountInfo{}, fmt.Errorf("%w: mount %q", ErrOfflineMountNotFound, cleaned)
}

func (c *Core) offlineDriver(name string) (drive.Driver, bool) {
	provider, ok := c.fs.(diagnostics.DriverProvider)
	if !ok {
		return nil, false
	}
	cleaned := vfstypes.CleanMountName(name)
	for _, named := range provider.Drivers() {
		if vfstypes.CleanMountName(named.Name) == cleaned {
			return named.Driver, true
		}
	}
	return nil, false
}

// offlineVirtualPath builds the filesystem path for a mount-relative
// directory in the Core's own view (namespace-prefixed or root-relative).
func (c *Core) offlineVirtualPath(mountName, dir string) string {
	dir = vfstypes.CleanVirtualPath(dir)
	if !c.offlineIsNamespace() {
		return dir
	}
	if dir == "/" {
		return "/" + mountName
	}
	return "/" + mountName + dir
}

// ensureParseDestination verifies (and optionally creates) an explicit parse
// directory and returns its provider identity. A missing directory fails unless
// creation was requested; the mount root is always accepted.
func (c *Core) ensureParseDestination(ctx context.Context, mountName, dir string, mkdir bool) (string, error) {
	if dir == "" {
		// Provider default (its magnet/system directory): nothing to check.
		return "", nil
	}
	dir = vfstypes.CleanVirtualPath(dir)
	path := c.offlineVirtualPath(mountName, dir)
	entry, err := c.fs.Stat(ctx, path)
	if err == nil {
		if !entry.IsDir {
			return "", fmt.Errorf("%w: %s is not a directory", ErrOfflineParseDestinationMissing, path)
		}
		return c.offlineDestinationIdentity(ctx, mountName, dir, entry.ID), nil
	}
	if !vfstypes.IsNotFound(err) {
		return "", fmt.Errorf("core: offline parse destination %s: %w", path, err)
	}
	if !mkdir {
		return "", fmt.Errorf("%w: %s (pass --mkdir to create it)", ErrOfflineParseDestinationMissing, path)
	}
	created, err := c.fs.Mkdir(ctx, path)
	if err != nil {
		return "", fmt.Errorf("core: create offline parse destination %s: %w", path, err)
	}
	return c.offlineDestinationIdentity(ctx, mountName, dir, created.ID), nil
}

// offlineDestinationIdentity maps a mounted destination to the identity the
// provider understands. In a namespace a mount root is reported as the
// synthetic "/MOUNT" id, which is not a provider directory fid, so the
// provider's own root id is resolved instead; a nested path already carries the
// provider id from the mount's own stat.
func (c *Core) offlineDestinationIdentity(ctx context.Context, mountName, dir, id string) string {
	if vfstypes.CleanVirtualPath(dir) != "/" {
		return id
	}
	driver, ok := c.offlineDriver(mountName)
	if !ok || !drive.HasCapability(driver, drive.CapabilityPathResolver) {
		return id
	}
	rootID, err := driver.ResolvePath(ctx, "/")
	if err != nil || rootID == "" {
		return id
	}
	return rootID
}

func (c *Core) checkOfflineSelection(driver drive.Driver, selection task.OfflineSelection) error {
	if selection.Mode == "" || selection.Mode == task.OfflineSelectionAll {
		return nil
	}
	if !drive.HasCapability(driver, drive.CapabilityOfflineSelection) {
		return fmt.Errorf("%w: mount provider does not support file selection", ErrOfflineUnsupported)
	}
	return nil
}

// resolveDownloadDestination resolves a fetch's final destination. A path that
// names a configured mount becomes a namespace destination; a path inside the
// Core's own FUSE mount tree is mapped back to its mount; anything else is a
// plain local path. It enforces the same-location rule and returns warnings.
func (c *Core) resolveDownloadDestination(spec offlineSpec, downloadTo string) (mount, dir string, warnings []string, err error) {
	resolved, err := c.offlineResolveDestination(downloadTo)
	if err != nil {
		return "", "", nil, err
	}
	if resolved.mount != "" {
		if resolved.mount == spec.parseMount && spec.parseDir != "" &&
			vfstypes.CleanVirtualPath(resolved.dir) == vfstypes.CleanVirtualPath(spec.parseDir) {
			return "", "", nil, fmt.Errorf("%w: /%s%s", ErrOfflineSameDestination, resolved.mount, vfstypes.CleanVirtualPath(resolved.dir))
		}
		if resolved.mount == spec.parseMount {
			warnings = append(warnings, fmt.Sprintf(
				"download destination /%s%s is on the same provider as the parse destination; the result will be downloaded and uploaded again",
				resolved.mount, vfstypes.CleanVirtualPath(resolved.dir)))
		}
	}
	return resolved.mount, resolved.dir, warnings, nil
}

type offlineDestination struct {
	mount string
	dir   string
}

// offlineResolveDestination maps a caller's final destination to a mount, or
// leaves mount empty for a plain local path.
func (c *Core) offlineResolveDestination(downloadTo string) (offlineDestination, error) {
	trimmed := strings.TrimSpace(downloadTo)
	if trimmed == "" {
		return offlineDestination{}, fmt.Errorf("%w: download destination is empty", task.ErrInvalidOperation)
	}
	if !strings.HasPrefix(trimmed, "/") {
		// A relative path is a plain local path.
		return offlineDestination{}, nil
	}
	cleaned := vfstypes.CleanVirtualPath(trimmed)
	if c.offlineIsNamespace() {
		if match := c.offlineMountNameForPath(cleaned); match != "" {
			_, dir, err := c.offlineSplitMountPath(cleaned)
			if err != nil {
				return offlineDestination{}, err
			}
			return offlineDestination{mount: match, dir: dir}, nil
		}
	}
	if prefix := c.offlineFuseMountPoint(); prefix != "" {
		if mapped, ok := mapFusePathToNamespace(prefix, trimmed); ok {
			if c.offlineIsNamespace() {
				if match := c.offlineMountNameForPath(mapped); match != "" {
					_, dir, err := c.offlineSplitMountPath(mapped)
					if err != nil {
						return offlineDestination{}, err
					}
					return offlineDestination{mount: match, dir: dir}, nil
				}
			}
		}
	}
	return offlineDestination{}, nil
}

func (c *Core) offlineMountNameForPath(path string) string {
	trimmed := strings.TrimPrefix(vfstypes.CleanVirtualPath(path), "/")
	if trimmed == "" {
		return ""
	}
	first, _, _ := strings.Cut(trimmed, "/")
	for _, mount := range c.offlineMounts() {
		if mount.Name == first && mount.State != "failed" {
			return first
		}
	}
	return ""
}

func (c *Core) offlineFuseMountPoint() string {
	return strings.TrimSpace(c.fuseMountPoint)
}

func mapFusePathToNamespace(mountPoint, path string) (string, bool) {
	mountPoint = strings.TrimRight(mountPoint, "/")
	if mountPoint == "" {
		return "", false
	}
	if path == mountPoint {
		return "/", true
	}
	prefix := mountPoint + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	return "/" + strings.TrimPrefix(path, prefix), true
}

// findActiveOfflineTask returns a non-terminal offline task whose fingerprint
// matches. The final destination is not part of the fingerprint, so two fetches
// share one staging task.
func (c *Core) findActiveOfflineTask(ctx context.Context, fingerprint string) (task.Task, bool) {
	if fingerprint == "" {
		return task.Task{}, false
	}
	manager, err := c.taskManager()
	if err != nil {
		return task.Task{}, false
	}
	existing, err := manager.ListTasks(ctx, task.Filter{Types: []task.Type{task.TypeOfflineDownload, task.TypeOfflineFetch}})
	if err != nil {
		return task.Task{}, false
	}
	for _, candidate := range existing {
		if offlineTaskTerminal(candidate.State) {
			continue
		}
		detail, ok := decodeOfflineDetail(candidate.Detail)
		if !ok {
			continue
		}
		if offlineFingerprint(detail) == fingerprint {
			return candidate, true
		}
	}
	return task.Task{}, false
}

func offlineTaskTerminal(state task.State) bool {
	switch state {
	case task.StateSucceeded, task.StatePartialFailed, task.StateFailed, task.StateCanceled:
		return true
	default:
		return false
	}
}

func (s offlineSpec) detail() offlineDetail {
	detail := offlineDetail{
		Source:         s.source,
		SourceKind:     string(s.sourceKind),
		SourceIdentity: s.identity,
		Title:          s.title,
		ParseMount:     s.parseMount,
		ParseDir:       s.parseDir,
		ParseRemoteID:  s.parseRemoteID,
		Selection:      offlineDetailSelectionFromTask(s.selection),
		Mkdir:          s.mkdir,
		Phase:          offlinePhaseStaging,
	}
	if s.fetch {
		detail.DownloadTo = s.downloadTo
		detail.DownloadMount = s.downloadMount
		detail.DownloadDir = s.downloadDir
		detail.KeepCloud = s.keepCloud
		detail.Accelerated = s.accelerated
		detail.TransferWorkers = s.transferWorkers
		detail.TransferChunk = s.transferChunk
	}
	return detail
}
