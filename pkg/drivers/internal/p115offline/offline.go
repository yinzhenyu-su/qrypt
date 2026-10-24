// Package p115offline adapts 115's task-list offline model to qrypt's
// provider-neutral offline contracts. The cookie and open-platform drivers
// share this lifecycle and supply a Backend for their own API and error
// surface.
package p115offline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// maxTreeDepth bounds a provider-reported result tree. 115 cannot create
// directory cycles, but a malformed or hostile listing must not recurse
// forever.
const maxTreeDepth = 64

// ErrTaskNotFound is returned by a Backend when the provider no longer lists a
// task. The adapter turns it into drive.OfflineErrProviderTaskMissing so Core
// can reconcile a finished task against the parse destination.
var ErrTaskNotFound = errors.New("p115offline: task not found")

// Task is the provider-neutral view of one 115 offline task. Backends decode
// their SDK response into this shape and normalize Percent to 0..100.
type Task struct {
	ID       string
	Name     string
	Size     int64
	Status   int
	Percent  float64
	FileID   string
	ParentID string
	URL      string
	Error    string
}

// Backend is the small provider-specific surface the shared adapter needs.
// Implementations own authentication, rate limiting, SDK decoding, and
// provider error classification.
type Backend interface {
	// AddTask submits one source to the directory and returns its info_hash.
	AddTask(ctx context.Context, source, dirID string) (string, error)
	// ListTasks returns one page of the account-wide task list and the total
	// page count. Page numbering starts at 1.
	ListTasks(ctx context.Context, page int) ([]Task, int, error)
	// DeleteTask removes the provider task without deleting staged files.
	DeleteTask(ctx context.Context, id string) error
	// ListDir lists one provider directory.
	ListDir(ctx context.Context, dirID string) ([]drive.Entry, error)
	// DownloadTarget resolves a stored file id to a provider download URL.
	DownloadTarget(ctx context.Context, fileID string, size int64) (drive.DownloadTarget, error)
	// DefaultDirID is the mount's provider root, used when the caller did not
	// resolve an explicit parse destination.
	DefaultDirID() string
}

// Adapter implements drive.OfflineDownloader and drive.OfflineFetcher over a
// 115 Backend.
type Adapter struct {
	backend Backend

	mu          sync.Mutex
	pageCache   map[string]int
	expectedDir map[string]string
	knownRoot   map[string]string
}

// New returns an adapter over backend.
func New(backend Backend) *Adapter {
	return &Adapter{
		backend:     backend,
		pageCache:   make(map[string]int),
		expectedDir: make(map[string]string),
		knownRoot:   make(map[string]string),
	}
}

// OfflineSourceKinds reports the normalized source kinds 115 accepts.
func (a *Adapter) OfflineSourceKinds() []drive.OfflineSourceKind {
	return []drive.OfflineSourceKind{
		drive.OfflineSourceMagnet,
		drive.OfflineSourceHTTP,
		drive.OfflineSourceFTP,
	}
}

// CreateOfflineDownload submits a source and returns the provider task id
// immediately. Core's runner observes the first real state right after.
func (a *Adapter) CreateOfflineDownload(ctx context.Context, req drive.OfflineDownloadRequest) (drive.OfflineDownload, error) {
	if a == nil || a.backend == nil {
		return drive.OfflineDownload{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "115: offline backend is not configured")
	}
	if req.Selection.Mode != "" && req.Selection.Mode != drive.OfflineSelectionAll {
		return drive.OfflineDownload{}, drive.NewOfflineError(
			drive.OfflineErrSelectionUnsupported, "", "115: offline file selection is not supported")
	}
	if req.SourceKind != "" && !supportedSourceKind(req.SourceKind) {
		return drive.OfflineDownload{}, drive.NewOfflineError(
			drive.OfflineErrSourceUnsupported, "", "115: unsupported offline source kind "+string(req.SourceKind))
	}
	dirID := strings.TrimSpace(req.DestinationID)
	if dirID == "" {
		dirID = strings.TrimSpace(a.backend.DefaultDirID())
	}
	if dirID == "" {
		return drive.OfflineDownload{}, drive.NewOfflineError(
			drive.OfflineErrProvider, "", "115: offline destination directory is unknown")
	}
	id, err := a.backend.AddTask(ctx, req.Source, dirID)
	if err != nil {
		return drive.OfflineDownload{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return drive.OfflineDownload{}, drive.NewOfflineError(
			drive.OfflineErrProvider, "", "115: add task returned no info hash")
	}
	a.setExpectedDir(id, dirID)
	return drive.OfflineDownload{
		ID:          id,
		State:       drive.OfflineStateQueued,
		Destination: dirID,
	}, nil
}

// GetOfflineDownload observes one task by info_hash.
func (a *Adapter) GetOfflineDownload(ctx context.Context, id string) (drive.OfflineDownload, error) {
	if a == nil || a.backend == nil {
		return drive.OfflineDownload{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "115: offline backend is not configured")
	}
	task, err := a.findTask(ctx, id)
	if err != nil {
		if errors.Is(err, ErrTaskNotFound) {
			return drive.OfflineDownload{}, drive.NewOfflineError(
				drive.OfflineErrProviderTaskMissing, "", "115: offline task not found")
		}
		return drive.OfflineDownload{}, err
	}
	if expected := a.expectedDirFor(id); expected != "" && task.ParentID != "" && task.ParentID != expected {
		return drive.OfflineDownload{}, drive.NewOfflineError(
			drive.OfflineErrDuplicate, "",
			fmt.Sprintf("115: task %s already exists in %s, expected %s", id, task.ParentID, expected))
	}
	state, ok := stateFor(task.Status)
	if !ok {
		return drive.OfflineDownload{}, drive.NewOfflineError(
			drive.OfflineErrProvider, strconv.Itoa(task.Status),
			fmt.Sprintf("115: unknown offline task status %d", task.Status))
	}
	parentID := task.ParentID
	if parentID == "" {
		parentID = a.expectedDirFor(id)
	}
	rootID := task.FileID
	if rootID == "" {
		rootID = a.rootFor(id)
	}
	observation := drive.OfflineDownload{
		ID:          id,
		State:       state,
		Progress:    drive.OfflineProgress{Percent: clampPercent(task.Percent)},
		Destination: parentID,
		RemoteID:    rootID,
	}
	switch state {
	case drive.OfflineStateFailed:
		message := task.Error
		if message == "" {
			message = fmt.Sprintf("115: offline task failed (status %d)", task.Status)
		}
		observation.ErrorCode = string(drive.OfflineErrProvider)
		observation.Error = message
		return observation, nil
	case drive.OfflineStateSucceeded:
		files, err := a.resolveFiles(ctx, parentID, rootID)
		if err != nil {
			return drive.OfflineDownload{}, err
		}
		observation.Available = true
		observation.Files = files
		observation.RemoteID = rootID
	}
	return observation, nil
}

// CancelOfflineDownload deletes the provider task and keeps staged files.
func (a *Adapter) CancelOfflineDownload(ctx context.Context, id string) error {
	if a == nil || a.backend == nil || id == "" {
		return nil
	}
	err := a.backend.DeleteTask(ctx, id)
	a.forgetTask(id)
	if err == nil || errors.Is(err, ErrTaskNotFound) {
		return nil
	}
	if offline, ok := drive.AsOfflineError(err); ok && offline.Code == drive.OfflineErrProviderTaskMissing {
		return nil
	}
	return err
}

// OfflineDownloadURL resolves one staged file to a provider download target.
// It is used when Core has no VFS staging path, notably a fetch selected only
// with --mount.
func (a *Adapter) OfflineDownloadURL(ctx context.Context, req drive.OfflineFetchRequest) (drive.OfflineDownloadTarget, error) {
	if a == nil || a.backend == nil {
		return drive.OfflineDownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "115: offline backend is not configured")
	}
	if len(req.Files) != 1 || strings.TrimSpace(req.Files[0].RemoteID) == "" {
		return drive.OfflineDownloadTarget{}, drive.NewOfflineError(
			drive.OfflineErrProvider, "", "115: offline fetch requires exactly one file id")
	}
	file := req.Files[0]
	target, err := a.backend.DownloadTarget(ctx, file.RemoteID, file.Size)
	if err != nil {
		return drive.OfflineDownloadTarget{}, err
	}
	if strings.TrimSpace(target.URL) == "" {
		return drive.OfflineDownloadTarget{}, drive.NewOfflineError(
			drive.OfflineErrProviderUnavailable, "", "115: download target has no URL")
	}
	if target.Size == 0 && file.Size > 0 {
		target.Size = file.Size
	}
	return target, nil
}

// RestoreOfflineLayout restores the output identity Core persisted so a fresh
// adapter can validate the parent directory and resolve the staged root after
// a restart.
func (a *Adapter) RestoreOfflineLayout(layout drive.OfflineLayout) {
	if a == nil || layout.TaskID == "" {
		return
	}
	if layout.ParentID != "" {
		a.setExpectedDir(layout.TaskID, layout.ParentID)
	}
	if layout.RootID != "" {
		a.mu.Lock()
		a.knownRoot[layout.TaskID] = layout.RootID
		a.mu.Unlock()
	}
}

func (a *Adapter) resolveFiles(ctx context.Context, parentID, rootID string) ([]drive.OfflineFile, error) {
	if parentID == "" || rootID == "" {
		return nil, drive.NewOfflineError(
			drive.OfflineErrProviderUnavailable, "", "115: task has no staged output identity yet")
	}
	entries, err := a.backend.ListDir(ctx, parentID)
	if err != nil {
		return nil, transient("list staging directory", err)
	}
	var root *drive.Entry
	for index := range entries {
		if entries[index].ID == rootID {
			root = &entries[index]
			break
		}
	}
	if root == nil {
		return nil, drive.NewOfflineError(
			drive.OfflineErrProviderUnavailable, "", "115: staged result is not visible yet")
	}
	if !root.IsDir {
		return []drive.OfflineFile{{
			Index:    0,
			Name:     root.Name,
			Size:     root.Size,
			RemoteID: root.ID,
			Path:     root.Name,
		}}, nil
	}
	files, err := a.walk(ctx, *root, root.Name, make(map[string]bool), 0)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, drive.NewOfflineError(
			drive.OfflineErrProvider, "", "115: completed offline task produced no files")
	}
	sort.Slice(files, func(left, right int) bool {
		return files[left].Path < files[right].Path
	})
	for index := range files {
		files[index].Index = index
	}
	return files, nil
}

func (a *Adapter) walk(ctx context.Context, dir drive.Entry, prefix string, visited map[string]bool, depth int) ([]drive.OfflineFile, error) {
	if depth > maxTreeDepth {
		return nil, drive.NewOfflineError(
			drive.OfflineErrProvider, "", "115: staged directory tree is too deep")
	}
	if dir.ID != "" {
		if visited[dir.ID] {
			return nil, drive.NewOfflineError(
				drive.OfflineErrProvider, "", "115: staged directory tree contains a cycle")
		}
		visited[dir.ID] = true
		defer delete(visited, dir.ID)
	}
	entries, err := a.backend.ListDir(ctx, dir.ID)
	if err != nil {
		return nil, transient("list staged directory", err)
	}
	files := make([]drive.OfflineFile, 0, len(entries))
	for _, entry := range entries {
		childPath := entry.Name
		if prefix != "" {
			childPath = prefix + "/" + entry.Name
		}
		if entry.IsDir {
			nested, err := a.walk(ctx, entry, childPath, visited, depth+1)
			if err != nil {
				return nil, err
			}
			files = append(files, nested...)
			continue
		}
		files = append(files, drive.OfflineFile{
			Name:     entry.Name,
			Size:     entry.Size,
			RemoteID: entry.ID,
			Path:     childPath,
		})
	}
	return files, nil
}

func (a *Adapter) findTask(ctx context.Context, id string) (Task, error) {
	if page, ok := a.cachedPage(id); ok && page > 1 {
		tasks, _, err := a.backend.ListTasks(ctx, page)
		if err != nil {
			return Task{}, err
		}
		if task, ok := matchTask(tasks, id); ok {
			return task, nil
		}
		a.forgetPage(id)
	}
	tasks, pageCount, err := a.backend.ListTasks(ctx, 1)
	if err != nil {
		return Task{}, err
	}
	if task, ok := matchTask(tasks, id); ok {
		a.rememberPage(id, 1)
		return task, nil
	}
	if pageCount < 1 {
		pageCount = 1
	}
	for page := 2; page <= pageCount; page++ {
		tasks, _, err := a.backend.ListTasks(ctx, page)
		if err != nil {
			return Task{}, err
		}
		if task, ok := matchTask(tasks, id); ok {
			a.rememberPage(id, page)
			return task, nil
		}
	}
	a.forgetPage(id)
	return Task{}, ErrTaskNotFound
}

func matchTask(tasks []Task, id string) (Task, bool) {
	for _, task := range tasks {
		if task.ID == id || strings.EqualFold(task.ID, id) {
			return task, true
		}
	}
	return Task{}, false
}

func stateFor(status int) (drive.OfflineState, bool) {
	switch status {
	case -1:
		return drive.OfflineStateFailed, true
	case 0:
		return drive.OfflineStateQueued, true
	case 1:
		return drive.OfflineStateRunning, true
	case 2:
		return drive.OfflineStateSucceeded, true
	default:
		return "", false
	}
}

func supportedSourceKind(kind drive.OfflineSourceKind) bool {
	switch kind {
	case drive.OfflineSourceMagnet, drive.OfflineSourceHTTP, drive.OfflineSourceFTP:
		return true
	default:
		return false
	}
}

func clampPercent(percent float64) float64 {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func transient(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if offline, ok := drive.AsOfflineError(err); ok {
		return offline
	}
	return drive.NewOfflineError(
		drive.OfflineErrProviderUnavailable, "",
		fmt.Sprintf("115: %s: %v", operation, err))
}

func (a *Adapter) cachedPage(id string) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	page, ok := a.pageCache[id]
	return page, ok
}

func (a *Adapter) rememberPage(id string, page int) {
	a.mu.Lock()
	a.pageCache[id] = page
	a.mu.Unlock()
}

func (a *Adapter) forgetPage(id string) {
	a.mu.Lock()
	delete(a.pageCache, id)
	a.mu.Unlock()
}

func (a *Adapter) setExpectedDir(id, dirID string) {
	a.mu.Lock()
	a.expectedDir[id] = dirID
	a.mu.Unlock()
}

func (a *Adapter) expectedDirFor(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.expectedDir[id]
}

func (a *Adapter) rootFor(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.knownRoot[id]
}

func (a *Adapter) forgetTask(id string) {
	a.mu.Lock()
	delete(a.pageCache, id)
	delete(a.expectedDir, id)
	delete(a.knownRoot, id)
	a.mu.Unlock()
}
