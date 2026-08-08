package mobile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/core"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all" // registers all drivers via their init functions
	"github.com/yinzhenyu/qrypt/pkg/media"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

type readCancels struct {
	mu     sync.Mutex
	active []struct {
		ctx    context.Context
		cancel context.CancelFunc
	}
}

// begin registers an in-flight read so CancelFileReadJSON /
// CancelVirtualReadJSON can abort it. The returned done function unregisters
// the read when it finishes. Future reads are unaffected by a cancel.
func (r *readCancels) begin(timeoutMS int) (context.Context, func()) {
	ctx, cancel := core.TimeoutContext(timeoutMS)
	r.mu.Lock()
	r.active = append(r.active, struct {
		ctx    context.Context
		cancel context.CancelFunc
	}{ctx: ctx, cancel: cancel})
	r.mu.Unlock()
	done := func() {
		r.mu.Lock()
		for i, item := range r.active {
			if item.ctx == ctx {
				r.active = append(r.active[:i], r.active[i+1:]...)
				break
			}
		}
		r.mu.Unlock()
	}
	return ctx, done
}

func (r *readCancels) cancelAll() {
	r.mu.Lock()
	active := r.active
	r.active = nil
	r.mu.Unlock()
	for _, item := range active {
		item.cancel()
	}
}

type entry struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	ID        string `json:"id,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	ModTime   string `json:"mod_time,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type session struct {
	core       *core.Core
	configPath string
	runtime    core.RuntimeLayout
	// ctx is the session lifecycle context: created at open, canceled at
	// close, so every API call and background task derived from it stops
	// when the session is torn down. Process-level initialization (Open/
	// Import) uses context.Background() as the parent; nothing else in
	// mobile should.
	ctx    context.Context
	cancel context.CancelFunc
	// mu guards core so a concurrent ReloadConfigJSON cannot swap or close
	// the core while another API call is using it.
	mu sync.RWMutex
}

// timeoutContext derives a call deadline from the session lifecycle context
// instead of context.Background(), so a closed session cancels in-flight
// calls even when their per-call deadline has not elapsed yet.
func (s *session) timeoutContext(timeoutMS int) (context.Context, context.CancelFunc) {
	if timeoutMS <= 0 {
		return context.WithCancel(s.ctx)
	}
	return context.WithTimeout(s.ctx, time.Duration(timeoutMS)*time.Millisecond)
}

// withCore runs fn while holding the session read lock. A concurrent
// ReloadConfigJSON takes the write lock, so the core pointer is stable for
// the whole call and never observed mid-close.
func withCore[T any](s *session, fn func(*core.Core) (T, error)) (T, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fn(s.core)
}

// withCoreErr is withCore for calls that only return an error.
func withCoreErr(s *session, fn func(*core.Core) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fn(s.core)
}

type fileHandle struct {
	coreID       string
	path         string
	size         int64
	readPriority vfs.ReadPriority
	reads        readCancels
}

type virtualHandle struct {
	coreID string
	file   media.VirtualFile
	reads  readCancels
}

type downloadStreamHandle struct {
	coreID string
	handle *core.DownloadStreamItemHandle
}

type uploadStreamItemHandle struct {
	coreID string
	handle *core.UploadStreamItemHandle
}

type taskEventHandle struct {
	coreID string
	sub    interface {
		Read(context.Context) ([]core.TaskEvent, error)
		ReadAvailable() ([]core.TaskEvent, error)
		Close()
	}
}

type runtimeJSON struct {
	ConfigDir string `json:"config_dir"`
	Storage   struct {
		ReadCacheDir string `json:"read_cache_dir"`
		ThumbnailDir string `json:"thumbnail_cache_dir"`
		UploadDir    string `json:"upload_dir"`
		StateDir     string `json:"state_dir"`
		LogDir       string `json:"log_dir"`
		TmpDir       string `json:"tmp_dir"`
	} `json:"storage"`
}

var registry = struct {
	mu          sync.Mutex
	sessions    map[string]*session
	files       map[string]*fileHandle
	virtuals    map[string]*virtualHandle
	downloads   map[string]*downloadStreamHandle
	taskUploads map[string]*uploadStreamItemHandle
	taskEvents  map[string]*taskEventHandle
}{
	sessions:    map[string]*session{},
	files:       map[string]*fileHandle{},
	virtuals:    map[string]*virtualHandle{},
	downloads:   map[string]*downloadStreamHandle{},
	taskUploads: map[string]*uploadStreamItemHandle{},
	taskEvents:  map[string]*taskEventHandle{},
}

func openCore(configPath, runtimeRaw string) (string, error) {
	runtime, err := parseRuntimeJSON(runtimeRaw)
	if err != nil {
		return "", wrapError(err)
	}
	return openWithRuntime(configPath, runtime)
}

func openWithRuntime(configPath string, runtime core.RuntimeLayout) (string, error) {
	c, err := core.Open(context.Background(), core.Options{
		ConfigPath: configPath,
		Runtime:    runtime,
	})
	if err != nil {
		return "", wrapError(err)
	}
	id, err := newID()
	if err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), coreCloseTimeout)
		_ = c.Close(closeCtx)
		closeCancel()
		return "", wrapError(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	registry.mu.Lock()
	registry.sessions[id] = &session{core: c, configPath: configPath, runtime: runtime, ctx: ctx, cancel: cancel}
	registry.mu.Unlock()
	return id, nil
}

func OpenJSON(configPath, runtimeRaw string) string {
	runtime, err := parseRuntimeJSON(runtimeRaw)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	id, err := openWithRuntime(configPath, runtime)
	return resultJSON(id, err)
}

func ImportConfigJSON(srcPath, runtimeRaw string) string {
	runtime, err := parseRuntimeJSON(runtimeRaw)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	path, err := core.ImportConfig(srcPath, runtime)
	return resultJSON(path, err)
}

func OpenImportedJSON(runtimeRaw string) string {
	runtime, err := parseRuntimeJSON(runtimeRaw)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	configPath, err := core.ImportedConfigPath(runtime)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	c, err := core.OpenImported(context.Background(), runtime)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	id, err := newID()
	if err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), coreCloseTimeout)
		_ = c.Close(closeCtx)
		closeCancel()
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := context.WithCancel(context.Background())
	registry.mu.Lock()
	registry.sessions[id] = &session{core: c, configPath: configPath, runtime: runtime, ctx: ctx, cancel: cancel}
	registry.mu.Unlock()
	return resultJSON(id, nil)
}

func parseRuntimeJSON(raw string) (core.RuntimeLayout, error) {
	var in runtimeJSON
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return core.RuntimeLayout{}, fmt.Errorf("mobile: invalid runtime json: %w", err)
	}
	runtime := core.RuntimeLayout{
		ConfigDir:    in.ConfigDir,
		ReadCacheDir: in.Storage.ReadCacheDir,
		ThumbnailDir: in.Storage.ThumbnailDir,
		UploadDir:    in.Storage.UploadDir,
		StateDir:     in.Storage.StateDir,
		LogDir:       in.Storage.LogDir,
		TmpDir:       in.Storage.TmpDir,
	}
	if runtime.ConfigDir == "" {
		return core.RuntimeLayout{}, fmt.Errorf("mobile: runtime config_dir required")
	}
	if runtime.ReadCacheDir == "" {
		return core.RuntimeLayout{}, fmt.Errorf("mobile: runtime storage.read_cache_dir required")
	}
	if runtime.ThumbnailDir == "" {
		return core.RuntimeLayout{}, fmt.Errorf("mobile: runtime storage.thumbnail_cache_dir required")
	}
	if runtime.UploadDir == "" {
		return core.RuntimeLayout{}, fmt.Errorf("mobile: runtime storage.upload_dir required")
	}
	if runtime.StateDir == "" {
		return core.RuntimeLayout{}, fmt.Errorf("mobile: runtime storage.state_dir required")
	}
	if runtime.LogDir == "" {
		return core.RuntimeLayout{}, fmt.Errorf("mobile: runtime storage.log_dir required")
	}
	if runtime.TmpDir == "" {
		return core.RuntimeLayout{}, fmt.Errorf("mobile: runtime storage.tmp_dir required")
	}
	return runtime, nil
}

// coreCloseTimeout bounds core shutdown during session close so a hung
// worker cannot block CloseJSON forever.
const coreCloseTimeout = 30 * time.Second

func closeCore(coreID string) error {
	registry.mu.Lock()
	s, ok := registry.sessions[coreID]
	if !ok {
		registry.mu.Unlock()
		return wrapError(fmt.Errorf("mobile: unknown core %q", coreID))
	}
	delete(registry.sessions, coreID)
	handles := collectCoreHandlesLocked(coreID)
	registry.mu.Unlock()
	closeCollectedHandles(handles)
	// Abort every in-flight API call derived from the session context, then
	// give the core a bounded window to stop workers and flush state.
	s.cancel()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), coreCloseTimeout)
	defer closeCancel()
	return withCoreErr(s, func(c *core.Core) error { return c.Close(closeCtx) })
}

func collectCoreHandlesLocked(coreID string) coreHandles {
	var handles coreHandles
	for id, handle := range registry.files {
		if handle.coreID == coreID {
			handle.reads.cancelAll()
			delete(registry.files, id)
		}
	}
	for id, handle := range registry.virtuals {
		if handle.coreID == coreID {
			handles.virtuals = append(handles.virtuals, handle)
			delete(registry.virtuals, id)
		}
	}
	for id, handle := range registry.downloads {
		if handle.coreID == coreID {
			handles.downloads = append(handles.downloads, handle.handle)
			delete(registry.downloads, id)
		}
	}
	for id, handle := range registry.taskUploads {
		if handle.coreID == coreID {
			handles.taskUploads = append(handles.taskUploads, handle.handle)
			delete(registry.taskUploads, id)
		}
	}
	for id, handle := range registry.taskEvents {
		if handle.coreID == coreID {
			handles.taskEvents = append(handles.taskEvents, handle.sub)
			delete(registry.taskEvents, id)
		}
	}
	return handles
}

type coreHandles struct {
	virtuals    []*virtualHandle
	downloads   []*core.DownloadStreamItemHandle
	taskUploads []*core.UploadStreamItemHandle
	taskEvents  []interface{ Close() }
}

func closeCollectedHandles(handles coreHandles) {
	for _, file := range handles.virtuals {
		file.reads.cancelAll()
		_ = file.file.Close()
	}
	for _, handle := range handles.downloads {
		_ = handle.Close()
	}
	for _, handle := range handles.taskUploads {
		_ = handle.Close()
	}
	for _, handle := range handles.taskEvents {
		handle.Close()
	}
}

func CloseJSON(coreID string) string {
	return resultJSON(nil, closeCore(coreID))
}

func ClassifyErrorMessageJSON(message string) string {
	return resultJSON(core.ClassifyErrorMessage(message), nil)
}

func DriverNamesJSON() string {
	raw, err := core.DriverNamesJSON()
	return rawResultJSON(raw, err)
}

func DriverSchemaJSON(name string) string {
	raw, err := core.DriverSchemaJSON(name)
	return rawResultJSON(raw, err)
}

func getSession(coreID string) (*session, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	s := registry.sessions[coreID]
	if s == nil {
		return nil, fmt.Errorf("mobile: unknown core %q", coreID)
	}
	return s, nil
}

func getFile(handleID string) (*fileHandle, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	handle := registry.files[handleID]
	if handle == nil {
		return nil, fmt.Errorf("mobile: unknown file handle %q", handleID)
	}
	return handle, nil
}

func getVirtualFile(handleID string) (*virtualHandle, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	handle := registry.virtuals[handleID]
	if handle == nil {
		return nil, fmt.Errorf("mobile: unknown virtual file handle %q", handleID)
	}
	return handle, nil
}

func getDownloadStream(handleID string) (*downloadStreamHandle, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	handle := registry.downloads[handleID]
	if handle == nil {
		return nil, fmt.Errorf("mobile: unknown download stream handle %q", handleID)
	}
	return handle, nil
}

func takeDownloadStream(handleID string) (*downloadStreamHandle, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	handle := registry.downloads[handleID]
	if handle == nil {
		return nil, fmt.Errorf("mobile: unknown download stream handle %q", handleID)
	}
	delete(registry.downloads, handleID)
	return handle, nil
}

func getUploadStreamItem(handleID string) (*uploadStreamItemHandle, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	handle := registry.taskUploads[handleID]
	if handle == nil {
		return nil, fmt.Errorf("mobile: unknown upload stream item handle %q", handleID)
	}
	return handle, nil
}

func takeUploadStreamItem(handleID string) (*uploadStreamItemHandle, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	handle := registry.taskUploads[handleID]
	if handle == nil {
		return nil, fmt.Errorf("mobile: unknown upload stream item handle %q", handleID)
	}
	delete(registry.taskUploads, handleID)
	return handle, nil
}

func getTaskEvent(handleID string) (*taskEventHandle, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	handle := registry.taskEvents[handleID]
	if handle == nil {
		return nil, fmt.Errorf("mobile: unknown task events handle %q", handleID)
	}
	return handle, nil
}

func takeTaskEvent(handleID string) (*taskEventHandle, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	handle := registry.taskEvents[handleID]
	if handle == nil {
		return nil, fmt.Errorf("mobile: unknown task events handle %q", handleID)
	}
	delete(registry.taskEvents, handleID)
	return handle, nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
