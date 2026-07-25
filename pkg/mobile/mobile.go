package mobile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/core"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all"
	"github.com/yinzhenyu/qrypt/pkg/media"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

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
	core *core.Core
}

type fileHandle struct {
	coreID       string
	path         string
	size         int64
	readPriority vfs.ReadPriority
}

type virtualHandle struct {
	coreID string
	file   media.VirtualFile
}

type streamingUploadHandle struct {
	mu     sync.Mutex
	coreID string
	path   string
	offset int64
	closed bool
}

type runtimeJSON struct {
	ConfigDir string `json:"config_dir"`
	Storage   struct {
		ReadCacheDir string `json:"read_cache_dir"`
		ThumbnailDir string `json:"thumbnail_cache_dir"`
		WritebackDir string `json:"writeback_dir"`
		StateDir     string `json:"state_dir"`
		LogDir       string `json:"log_dir"`
		TmpDir       string `json:"tmp_dir"`
	} `json:"storage"`
}

var registry = struct {
	mu       sync.Mutex
	sessions map[string]*session
	files    map[string]*fileHandle
	virtuals map[string]*virtualHandle
	uploads  map[string]*streamingUploadHandle
}{
	sessions: map[string]*session{},
	files:    map[string]*fileHandle{},
	virtuals: map[string]*virtualHandle{},
	uploads:  map[string]*streamingUploadHandle{},
}

func Open(configPath, runtimeRaw string) (string, error) {
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
		_ = c.Close(context.Background())
		return "", wrapError(err)
	}
	registry.mu.Lock()
	registry.sessions[id] = &session{core: c}
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
	c, err := core.OpenImported(context.Background(), runtime)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	id, err := newID()
	if err != nil {
		_ = c.Close(context.Background())
		return resultJSON(nil, wrapError(err))
	}
	registry.mu.Lock()
	registry.sessions[id] = &session{core: c}
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
		WritebackDir: in.Storage.WritebackDir,
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
	if runtime.WritebackDir == "" {
		return core.RuntimeLayout{}, fmt.Errorf("mobile: runtime storage.writeback_dir required")
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

func Close(coreID string) error {
	registry.mu.Lock()
	s, ok := registry.sessions[coreID]
	if !ok {
		registry.mu.Unlock()
		return wrapError(fmt.Errorf("mobile: unknown core %q", coreID))
	}
	delete(registry.sessions, coreID)
	for id, handle := range registry.files {
		if handle.coreID == coreID {
			delete(registry.files, id)
		}
	}
	virtuals := make([]media.VirtualFile, 0)
	for id, handle := range registry.virtuals {
		if handle.coreID == coreID {
			virtuals = append(virtuals, handle.file)
			delete(registry.virtuals, id)
		}
	}
	for id, handle := range registry.uploads {
		if handle.coreID == coreID {
			delete(registry.uploads, id)
		}
	}
	registry.mu.Unlock()
	for _, file := range virtuals {
		_ = file.Close()
	}
	return s.core.Close(context.Background())
}

func CloseJSON(coreID string) string {
	return resultJSON(nil, Close(coreID))
}

func ClassifyErrorMessage(message string) (string, error) {
	data, err := json.Marshal(core.ClassifyErrorMessage(message))
	if err != nil {
		return "", err
	}
	return string(data), nil
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

func getStreamingUpload(handleID string) (*streamingUploadHandle, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	handle := registry.uploads[handleID]
	if handle == nil {
		return nil, fmt.Errorf("mobile: unknown streaming upload handle %q", handleID)
	}
	return handle, nil
}

func takeStreamingUpload(handleID string) (*streamingUploadHandle, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	handle := registry.uploads[handleID]
	if handle == nil {
		return nil, fmt.Errorf("mobile: unknown streaming upload handle %q", handleID)
	}
	delete(registry.uploads, handleID)
	return handle, nil
}

func removeStreamingUpload(handleID string, handle *streamingUploadHandle) {
	registry.mu.Lock()
	if registry.uploads[handleID] == handle {
		delete(registry.uploads, handleID)
	}
	registry.mu.Unlock()
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
