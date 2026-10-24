package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Core-level share receive problems.
var (
	// ErrShareLinkInvalid means the link cannot be parsed or no configured
	// driver recognizes it.
	ErrShareLinkInvalid = errors.New("core: share link is invalid")
	// ErrShareLinkProviderMismatch means the acting mount's provider does not
	// match the link.
	ErrShareLinkProviderMismatch = errors.New("core: share link belongs to another provider")
	// ErrShareCrossProviderUnsupported means the destination mount is a
	// different provider; a native save only exists inside one provider.
	ErrShareCrossProviderUnsupported = errors.New("core: cross-provider receive is unsupported")
	// ErrShareMountConflict means --mount and --to named different mounts.
	ErrShareMountConflict = errors.New("core: share mount selection conflict")
	// ErrShareDestinationMissing means the cloud destination does not exist and
	// creation was not requested.
	ErrShareDestinationMissing = errors.New("core: share destination is missing")
)

// sharePhase is the durable phase of a share task. It survives restart so a
// resumed save does not repeat provider work it already finished.
type sharePhase string

const (
	sharePhaseOpen     sharePhase = "open"
	sharePhaseSave     sharePhase = "save"
	sharePhaseVerify   sharePhase = "verify"
	sharePhaseDownload sharePhase = "download"
	sharePhaseCleanup  sharePhase = "cleanup"
	sharePhaseComplete sharePhase = "complete"
)

func (p sharePhase) display() task.Phase {
	switch p {
	case sharePhaseOpen, sharePhaseSave, sharePhaseVerify:
		return task.PhaseServerStage
	case sharePhaseDownload:
		return task.PhaseDownload
	case sharePhaseCleanup:
		return task.PhaseDelete
	case sharePhaseComplete:
		return task.PhaseComplete
	default:
		return task.PhasePending
	}
}

// shareDestEntry is the persisted form of a resolved destination directory.
// Entry.Extra is not JSON-serializable by contract, so provider-specific
// location data (a drive or space id) is captured here explicitly.
type shareDestEntry struct {
	ID       string          `json:"id"`
	ParentID string          `json:"parent_id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Extra    json.RawMessage `json:"extra,omitempty"`
}

func shareDestEntryFrom(entry drive.Entry) shareDestEntry {
	out := shareDestEntry{ID: entry.ID, ParentID: entry.ParentID, Name: entry.Name}
	if entry.Extra != nil {
		if data, err := json.Marshal(entry.Extra); err == nil && string(data) != "null" {
			out.Extra = data
		}
	}
	return out
}

func (e shareDestEntry) toDrive() drive.Entry {
	out := drive.Entry{ID: e.ID, ParentID: e.ParentID, Name: e.Name, IsDir: true}
	if len(e.Extra) > 0 {
		var extra any
		if err := json.Unmarshal(e.Extra, &extra); err == nil {
			out.Extra = extra
		}
	}
	return out
}

// shareSavedEntry is one entry's persisted save outcome.
type shareSavedEntry struct {
	SourceID     string `json:"source_id,omitempty"`
	Name         string `json:"name,omitempty"`
	Size         int64  `json:"size,omitempty"`
	RemoteID     string `json:"remote_id,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	Retryable    bool   `json:"retryable,omitempty"`
}

// shareObservation is the latest provider observation folded into the detail.
type shareObservation struct {
	State               string    `json:"state,omitempty"`
	Progress            float64   `json:"progress,omitempty"`
	DestinationVerified bool      `json:"destination_verified,omitempty"`
	ErrorCode           string    `json:"error_code,omitempty"`
	ProviderError       string    `json:"provider_error,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

// shareDetail is the durable, provider-neutral metadata of a receive task. It
// lives under Task.Detail[task.ShareDetailKey].
type shareDetail struct {
	Link     string `json:"link"`
	LinkID   string `json:"link_id,omitempty"`
	Mount    string `json:"mount,omitempty"`
	Passcode string `json:"passcode,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Title    string `json:"title,omitempty"`

	Selection offlineDetailSelection `json:"selection,omitempty"`

	ToMount    string         `json:"to_mount,omitempty"`
	ToDir      string         `json:"to_dir,omitempty"`
	ToEntry    shareDestEntry `json:"to_entry,omitempty"`
	ToRemoteID string         `json:"to_remote_id,omitempty"`
	Mkdir      bool           `json:"mkdir,omitempty"`
	// ToRequested records whether the caller asked for a cloud save at all.
	// A fetch without --to downloads straight from the share.
	ToRequested bool `json:"to_requested,omitempty"`

	SaveHandle string            `json:"save_handle,omitempty"`
	SaveState  string            `json:"save_state,omitempty"`
	Saved      []shareSavedEntry `json:"saved,omitempty"`
	// SelectedEntries is the exact provider selection submitted for the save.
	// Persisting it lets a restarted task poll a known handle without reopening
	// or relisting the share, which may be expired or changed by then.
	SelectedEntries []shareSelectedEntry `json:"selected_entries,omitempty"`

	DownloadTo    string `json:"download_to,omitempty"`
	DownloadMount string `json:"download_mount,omitempty"`
	DownloadDir   string `json:"download_dir,omitempty"`
	KeepCloud     bool   `json:"keep_cloud,omitempty"`
	// Transferred is the persisted per-file download outcome of a fetch.
	Transferred []shareSavedEntry `json:"transferred,omitempty"`
	Workers     int               `json:"workers,omitempty"`
	Chunk       int64             `json:"chunk,omitempty"`

	Phase       sharePhase        `json:"phase,omitempty"`
	Observation *shareObservation `json:"observation,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
}

// shareSelectedEntry is the durable identity of one selected share entry. Only
// the fields a provider needs to map a save result are kept.
type shareSelectedEntry struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Name     string `json:"name,omitempty"`
	Size     int64  `json:"size,omitempty"`
	IsDir    bool   `json:"is_dir,omitempty"`
	Ticket   string `json:"ticket,omitempty"`
	RootID   string `json:"root_id,omitempty"`
}

func shareSelectedEntries(entries []drive.ShareEntry) []shareSelectedEntry {
	out := make([]shareSelectedEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, shareSelectedEntry{
			ID:       entry.ID,
			ParentID: entry.ParentID,
			Name:     entry.Name,
			Size:     entry.Size,
			IsDir:    entry.IsDir,
			Ticket:   entry.Ticket,
			RootID:   entry.RootID,
		})
	}
	return out
}

func (e shareSelectedEntry) driveEntry() drive.ShareEntry {
	return drive.ShareEntry{
		Entry: drive.Entry{
			ID:       e.ID,
			ParentID: e.ParentID,
			Name:     e.Name,
			Size:     e.Size,
			IsDir:    e.IsDir,
		},
		Ticket: e.Ticket,
		RootID: e.RootID,
	}
}

func shareSelectedDriveEntries(entries []shareSelectedEntry) []drive.ShareEntry {
	out := make([]drive.ShareEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.driveEntry())
	}
	return out
}

func (d shareDetail) selectionTask() task.OfflineSelection {
	return task.OfflineSelection{
		Mode:    task.OfflineSelectionMode(d.Selection.Mode),
		Largest: d.Selection.Largest,
		Include: d.Selection.Include,
		Exclude: d.Selection.Exclude,
	}
}

func (d shareDetail) link() drive.ShareLink {
	return drive.ShareLink{URL: d.Link, LinkID: d.LinkID, Passcode: d.Passcode}
}

func (d shareDetail) apply(item *task.Task) {
	if item.Detail == nil {
		item.Detail = map[string]any{}
	}
	item.Detail[task.ShareDetailKey] = d
	item.Detail["phase"] = d.Phase.display()
}

func decodeShareDetail(detail map[string]any) (shareDetail, bool) {
	if detail == nil {
		return shareDetail{}, false
	}
	value, ok := detail[task.ShareDetailKey]
	if !ok || value == nil {
		return shareDetail{}, false
	}
	switch typed := value.(type) {
	case shareDetail:
		return typed, true
	case *shareDetail:
		if typed == nil {
			return shareDetail{}, false
		}
		return *typed, true
	}
	data, err := json.Marshal(value)
	if err != nil {
		return shareDetail{}, false
	}
	var decoded shareDetail
	if err := json.Unmarshal(data, &decoded); err != nil {
		return shareDetail{}, false
	}
	return decoded, true
}

// shareReceiveSpec is a fully resolved receive request: the link, the acting
// mount, the selection, and the cloud destination.
type shareReceiveSpec struct {
	link          drive.ShareLink
	mount         string
	driver        drive.Driver
	policy        drive.SharePolicy
	selection     task.OfflineSelection
	toMount       string
	toDir         string
	toEntry       drive.Entry
	toRemoteID    string
	mkdir         bool
	toRequested   bool
	newTask       bool
	downloadTo    string
	downloadMount string
	downloadDir   string
	keepCloud     bool
	workers       int
	chunk         int64
	warnings      []string
}

func (s shareReceiveSpec) detail() shareDetail {
	return shareDetail{
		Link:          s.link.URL,
		LinkID:        s.link.LinkID,
		Mount:         s.mount,
		Passcode:      s.link.Passcode,
		Selection:     offlineDetailSelectionFromTask(s.selection),
		ToMount:       s.toMount,
		ToDir:         s.toDir,
		ToEntry:       shareDestEntryFrom(s.toEntry),
		ToRemoteID:    s.toRemoteID,
		Mkdir:         s.mkdir,
		ToRequested:   s.toRequested,
		DownloadTo:    s.downloadTo,
		DownloadMount: s.downloadMount,
		DownloadDir:   s.downloadDir,
		KeepCloud:     s.keepCloud,
		Workers:       s.workers,
		Chunk:         s.chunk,
		Phase:         sharePhaseOpen,
		Warnings:      s.warnings,
	}
}

// createShareSaveTask creates a provider-side copy of share content into the
// acting account's drive.
func (c *Core) createShareSaveTask(ctx context.Context, req task.Request) (task.Task, error) {
	return c.createShareTask(ctx, req, false)
}

// createShareFetchTask creates a receive that saves (when a cloud destination
// is present) and then downloads the result locally.
func (c *Core) createShareFetchTask(ctx context.Context, req task.Request) (task.Task, error) {
	return c.createShareTask(ctx, req, true)
}

func (c *Core) createShareTask(ctx context.Context, req task.Request, fetch bool) (task.Task, error) {
	if c == nil || c.fs == nil {
		return task.Task{}, fmt.Errorf("core: closed")
	}
	if req.Share == nil {
		return task.Task{}, fmt.Errorf("%w: share inputs are required", task.ErrInvalidOperation)
	}
	if err := req.Share.Validate(); err != nil {
		return task.Task{}, err
	}
	if !fetch && strings.TrimSpace(req.Share.DownloadTo) != "" {
		return task.Task{}, fmt.Errorf("%w: a download destination selects share fetch", task.ErrInvalidOperation)
	}
	if fetch && strings.TrimSpace(req.Share.DownloadTo) == "" {
		return task.Task{}, fmt.Errorf("%w: share fetch requires a download destination", task.ErrInvalidOperation)
	}
	spec, err := c.resolveShareReceiveSpec(ctx, *req.Share, fetch)
	if err != nil {
		return task.Task{}, err
	}

	detail := spec.detail()
	manager, err := c.taskManager()
	if err != nil {
		return task.Task{}, err
	}
	fingerprint := shareReceiveFingerprint(detail)

	typ := task.TypeShareSave
	if fetch {
		typ = task.TypeShareFetch
	}
	now := offlineNow()
	item := task.Task{
		ID:           newShareTaskID(),
		Type:         typ,
		State:        task.StateQueued,
		Scope:        task.ScopeForType(typ),
		Visibility:   task.VisibilityForType(typ),
		Mount:        spec.mount,
		CreatedAt:    now,
		UpdatedAt:    now,
		Capabilities: taskCreationCapabilities(typ),
		Progress: task.Progress{
			Phase: sharePhaseOpen.display(),
		},
	}
	detail.apply(&item)
	applyTaskRequestMetadata(&item, req)
	if item.OperationFingerprint == "" {
		item.OperationFingerprint = fingerprint
	}
	operationFingerprint := item.OperationFingerprint

	c.shareCreateMu.Lock()
	defer c.shareCreateMu.Unlock()

	if req.IdempotencyKey != "" {
		keyed, found, keyErr := manager.FindIdempotent(item)
		if keyErr != nil {
			return task.Task{}, keyErr
		}
		if found {
			return keyed, nil
		}
	}
	if !spec.newTask {
		if existing, ok := c.findActiveShareTask(ctx, fingerprint); ok {
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
		return c.runShareTask(runCtx, update, item.ID, fetch)
	}
	return manager.SubmitIdempotent(ctx, item, run)
}

// shareReceiveFingerprint identifies an equivalent receive for de-duplication:
// the link identity, acting mount, selection, cloud destination, and — for a
// fetch — the local download destination. Two fetches that write different
// directories are different work and must not share a task. Local cleanup
// preferences and transfer tuning stay excluded.
func shareReceiveFingerprint(detail shareDetail) string {
	payload := struct {
		Mount         string                 `json:"mount"`
		LinkID        string                 `json:"link_id"`
		Selection     offlineDetailSelection `json:"selection"`
		ToMount       string                 `json:"to_mount"`
		ToDir         string                 `json:"to_dir"`
		ToRemoteID    string                 `json:"to_remote_id"`
		DownloadTo    string                 `json:"download_to,omitempty"`
		DownloadMount string                 `json:"download_mount,omitempty"`
		DownloadDir   string                 `json:"download_dir,omitempty"`
	}{
		Mount:         detail.Mount,
		LinkID:        detail.LinkID,
		Selection:     detail.Selection,
		ToMount:       detail.ToMount,
		ToDir:         detail.ToDir,
		ToRemoteID:    detail.ToRemoteID,
		DownloadTo:    detail.DownloadTo,
		DownloadMount: detail.DownloadMount,
		DownloadDir:   detail.DownloadDir,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// resolveShareReceiveSpec resolves the link's provider, the acting mount, and
// the cloud destination, rejecting anything the provider cannot do before a
// task exists.
func (c *Core) resolveShareReceiveSpec(ctx context.Context, req task.ShareOperationRequest, fetch bool) (shareReceiveSpec, error) {
	link, mount, driver, policy, err := c.resolveShareLinkTarget(req.Link, req.Mount, req.To)
	if err != nil {
		return shareReceiveSpec{}, err
	}
	// An explicit --passcode wins over the link's ?pwd= query.
	if passcode := strings.TrimSpace(req.Passcode); passcode != "" {
		link.Passcode = passcode
	}
	// A receive only demands the capabilities it actually uses: a cloud save
	// needs ShareSave, a direct download needs ShareFetch, and both need
	// ShareReceive to open and browse the link.
	toRequested := !fetch || strings.TrimSpace(req.To) != ""
	if !drive.HasCapability(driver, drive.CapabilityShareReceive) {
		return shareReceiveSpec{}, fmt.Errorf("%w: mount %q", ErrShareUnsupported, mount)
	}
	if toRequested && !drive.HasCapability(driver, drive.CapabilityShareSave) {
		return shareReceiveSpec{}, fmt.Errorf("%w: mount %q", ErrShareUnsupported, mount)
	}
	if !toRequested && !drive.HasCapability(driver, drive.CapabilityShareFetch) {
		return shareReceiveSpec{}, fmt.Errorf("%w: mount %q", ErrShareUnsupported, mount)
	}
	if shareSelectionNeedsSupport(req.Selection) && !policy.Selection {
		return shareReceiveSpec{}, drive.NewShareError(drive.ShareErrUnsupported, "", "this provider does not support file selection")
	}
	mountInfo, err := c.offlineMount(mount)
	if err != nil {
		return shareReceiveSpec{}, err
	}
	// A provider-side save writes plaintext the acting mount would later try
	// to decrypt, so a cloud destination inside an encrypted mount is
	// rejected. A direct download that only reads the share and writes a local
	// destination stays allowed, because those bytes go through qrypt.
	if mountInfo.Encrypted && toRequested {
		return shareReceiveSpec{}, fmt.Errorf("%w: mount %q", ErrShareEncryptedDestination, mount)
	}
	// Validate the link before a durable task exists: a missing or wrong
	// passcode (and a dead link) must fail the request, not leave a failed task
	// behind. The session token is deliberately discarded; the runner opens the
	// link again rather than persisting provider credentials.
	if err := c.preflightShareSession(ctx, mount, driver, link); err != nil {
		return shareReceiveSpec{}, err
	}

	toDir := "/"
	if strings.TrimSpace(req.To) != "" {
		if _, dir, splitErr := c.offlineSplitMountPath(req.To); splitErr == nil {
			toDir = dir
		} else {
			return shareReceiveSpec{}, splitErr
		}
	}
	entry, remoteID, err := c.resolveShareDestination(ctx, mount, driver, toDir, req.Mkdir)
	if err != nil {
		return shareReceiveSpec{}, err
	}
	downloadMount, downloadDir := "", ""
	if strings.TrimSpace(req.DownloadTo) != "" {
		resolved, resolveErr := c.offlineResolveDestination(req.DownloadTo)
		if resolveErr != nil {
			return shareReceiveSpec{}, resolveErr
		}
		downloadMount, downloadDir = resolved.mount, resolved.dir
	}

	return shareReceiveSpec{
		link:          link,
		mount:         mount,
		driver:        driver,
		policy:        policy,
		selection:     req.Selection,
		toMount:       mount,
		toDir:         vfstypes.CleanVirtualPath(toDir),
		toEntry:       entry,
		toRemoteID:    remoteID,
		mkdir:         req.Mkdir,
		toRequested:   !fetch || strings.TrimSpace(req.To) != "",
		newTask:       req.New,
		downloadTo:    req.DownloadTo,
		downloadMount: downloadMount,
		downloadDir:   downloadDir,
		keepCloud:     req.KeepCloud,
		workers:       req.Workers,
		chunk:         req.ChunkSize,
	}, nil
}

// shareSelectionNeedsSupport reports whether a selection asks for more than
// "all", and therefore needs a provider that declares selection support.
func shareSelectionNeedsSupport(selection task.OfflineSelection) bool {
	if selection.Largest > 0 || len(selection.Include) > 0 || len(selection.Exclude) > 0 {
		return true
	}
	switch selection.Mode {
	case "", task.OfflineSelectionAll:
		return false
	default:
		return true
	}
}

// preflightShareSession opens the link before a durable receive task is
// created, so a missing passcode, a wrong passcode, an expired link, or a dead
// link is reported to the caller without leaving a failed task behind. The
// provider session token is discarded: it is a secret and the runner opens the
// link again rather than persisting it.
func (c *Core) preflightShareSession(ctx context.Context, mount string, driver drive.Driver, link drive.ShareLink) error {
	receiver, ok := driver.(drive.ShareReceiver)
	if !ok {
		return fmt.Errorf("%w: mount %q", ErrShareUnsupported, mount)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.shareLinkTimeout())
	defer cancel()
	session, err := receiver.OpenShare(callCtx, link)
	if err != nil {
		return shareCallError(callCtx, neutralPasscodeError(err))
	}
	if strings.TrimSpace(session.SessionToken) == "" {
		return drive.NewShareError(drive.ShareErrProvider, "", "provider returned no session token")
	}
	return nil
}

// resolveShareLinkTarget finds the driver that recognizes the link and the
// mount that acts on it. --mount wins over --to; an ambiguous provider with
// several mounts is rejected instead of guessing the account.
func (c *Core) resolveShareLinkTarget(link, mountName, to string) (drive.ShareLink, string, drive.Driver, drive.SharePolicy, error) {
	raw := strings.TrimSpace(link)
	if raw == "" {
		return drive.ShareLink{}, "", nil, drive.SharePolicy{}, fmt.Errorf("%w: share link is required", task.ErrInvalidOperation)
	}

	type candidate struct {
		mount  string
		driver drive.Driver
		link   drive.ShareLink
		policy drive.SharePolicy
	}
	var candidates []candidate
	for _, info := range c.offlineMounts() {
		if info.State == "failed" {
			continue
		}
		driver, ok := c.offlineDriver(info.Name)
		if !ok {
			continue
		}
		parser, ok := driver.(drive.ShareLinkParser)
		if !ok {
			continue
		}
		parsed, ok := parser.ParseShareLink(raw)
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{
			mount:  info.Name,
			driver: driver,
			link:   parsed,
			policy: drive.SharePolicyFor(driver),
		})
	}
	if len(candidates) == 0 {
		return drive.ShareLink{}, "", nil, drive.SharePolicy{}, fmt.Errorf("%w: %s", ErrShareLinkInvalid, raw)
	}

	toMount := ""
	if strings.TrimSpace(to) != "" {
		m, _, err := c.offlineSplitMountPath(to)
		if err != nil {
			return drive.ShareLink{}, "", nil, drive.SharePolicy{}, err
		}
		toMount = m
	}
	explicit := vfstypes.CleanMountName(mountName)
	if explicit != "" && toMount != "" && explicit != toMount {
		return drive.ShareLink{}, "", nil, drive.SharePolicy{}, fmt.Errorf("%w: --mount %q with destination on mount %q", ErrShareMountConflict, explicit, toMount)
	}

	wanted := toMount
	if wanted == "" {
		wanted = explicit
	}
	if wanted != "" {
		for _, candidate := range candidates {
			if candidate.mount == wanted {
				return candidate.link, candidate.mount, candidate.driver, candidate.policy, nil
			}
		}
		if toMount != "" {
			return drive.ShareLink{}, "", nil, drive.SharePolicy{}, fmt.Errorf("%w: mount %q", ErrShareCrossProviderUnsupported, toMount)
		}
		return drive.ShareLink{}, "", nil, drive.SharePolicy{}, fmt.Errorf("%w: mount %q", ErrShareLinkProviderMismatch, wanted)
	}

	if len(candidates) == 1 {
		candidate := candidates[0]
		return candidate.link, candidate.mount, candidate.driver, candidate.policy, nil
	}
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, candidate.mount)
	}
	return drive.ShareLink{}, "", nil, drive.SharePolicy{}, fmt.Errorf("%w: candidates: %s", ErrShareMountRequired, strings.Join(names, ", "))
}

// resolveShareDestination resolves the cloud destination directory, creating it
// only when asked. The mount root resolves to the provider's own root id.
func (c *Core) resolveShareDestination(ctx context.Context, mount string, driver drive.Driver, dir string, mkdir bool) (drive.Entry, string, error) {
	dir = vfstypes.CleanVirtualPath(dir)
	if dir == "/" {
		root := shareProviderRootID(ctx, driver)
		return drive.Entry{ID: root, Name: "/", IsDir: true}, root, nil
	}
	path := c.offlineVirtualPath(mount, dir)
	entry, err := c.fs.Stat(ctx, path)
	if err == nil {
		if !entry.IsDir {
			return drive.Entry{}, "", fmt.Errorf("%w: %s is not a directory", ErrShareDestinationMissing, path)
		}
		return entry, entry.ID, nil
	}
	if !vfstypes.IsNotFound(err) {
		return drive.Entry{}, "", fmt.Errorf("core: share destination %s: %w", path, err)
	}
	if !mkdir {
		return drive.Entry{}, "", fmt.Errorf("%w: %s (pass --mkdir to create it)", ErrShareDestinationMissing, path)
	}
	created, err := c.fs.Mkdir(ctx, path)
	if err != nil {
		return drive.Entry{}, "", fmt.Errorf("core: create share destination %s: %w", path, err)
	}
	return created, created.ID, nil
}

// shareProviderRootID resolves a mount's provider root. Providers that do not
// implement path resolution fall back to qrypt's root id convention.
func shareProviderRootID(ctx context.Context, driver drive.Driver) string {
	if driver != nil {
		if id, err := driver.ResolvePath(ctx, "/"); err == nil {
			id = strings.TrimSpace(id)
			if id != "" && !strings.HasPrefix(id, "/") {
				return id
			}
		}
	}
	return "0"
}

// ShareInspectRequest and ShareInspectResult are the shared contract types.
type (
	ShareInspectRequest = shareapi.InspectRequest
	ShareInspectResult  = shareapi.InspectResult
)

// InspectShareLink opens a share link and lists one directory. It is read-only:
// no task, no save, and no persisted session token.
func (c *Core) InspectShareLink(ctx context.Context, req ShareInspectRequest) (ShareInspectResult, error) {
	link, mount, driver, _, err := c.resolveShareLinkTarget(req.Link, req.Mount, "")
	if err != nil {
		return ShareInspectResult{}, err
	}
	if passcode := strings.TrimSpace(req.Passcode); passcode != "" {
		link.Passcode = passcode
	}
	if !drive.HasCapability(driver, drive.CapabilityShareReceive) {
		return ShareInspectResult{}, fmt.Errorf("%w: mount %q", ErrShareUnsupported, mount)
	}
	receiver, ok := driver.(drive.ShareReceiver)
	if !ok {
		return ShareInspectResult{}, fmt.Errorf("%w: mount %q", ErrShareUnsupported, mount)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.shareLinkTimeout())
	defer cancel()
	session, err := receiver.OpenShare(callCtx, link)
	if err != nil {
		return ShareInspectResult{}, shareCallError(callCtx, neutralPasscodeError(err))
	}
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		dir = "0"
	}
	page, err := receiver.ListShareEntries(callCtx, session, drive.SharePath{ID: dir}, req.Cursor)
	if err != nil {
		return ShareInspectResult{}, shareCallError(callCtx, err)
	}
	if dir == "0" && len(page.Entries) == 0 && page.Next == "" {
		// The provider's client treats an empty share root as deleted content;
		// qrypt reports the same fact instead of an empty directory.
		return ShareInspectResult{}, drive.NewShareError(drive.ShareErrNotFound, "", "the link has no visible content")
	}
	return ShareInspectResult{Link: link, Share: session.Share, Entries: page.Entries, Next: page.Next}, nil
}

// findActiveShareTask reuses a matching non-terminal receive. The fingerprint is
// recomputed from the stored detail, so a retry or a restart compares the same
// identity a new request would have.
func (c *Core) findActiveShareTask(ctx context.Context, fingerprint string) (task.Task, bool) {
	if fingerprint == "" {
		return task.Task{}, false
	}
	manager, err := c.taskManager()
	if err != nil {
		return task.Task{}, false
	}
	existing, err := manager.ListTasks(ctx, task.Filter{Types: []task.Type{task.TypeShareSave, task.TypeShareFetch}})
	if err != nil {
		return task.Task{}, false
	}
	for _, candidate := range existing {
		if offlineTaskTerminal(candidate.State) {
			continue
		}
		detail, ok := decodeShareDetail(candidate.Detail)
		if !ok {
			continue
		}
		if shareReceiveFingerprint(detail) == fingerprint {
			return candidate, true
		}
	}
	return task.Task{}, false
}

func newShareTaskID() string {
	var buffer [8]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "share-" + strconv.FormatInt(offlineNow().UnixNano(), 36)
	}
	return "share-" + hex.EncodeToString(buffer[:])
}
