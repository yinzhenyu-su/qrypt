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
	"github.com/yinzhenyu/qrypt/pkg/drive"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all"
	"github.com/yinzhenyu/qrypt/pkg/media"
)

type entry struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	ID       string `json:"id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size"`
	ModTime  string `json:"mod_time,omitempty"`
}

type session struct {
	core *core.Core
}

type fileHandle struct {
	coreID string
	path   string
	size   int64
}

type virtualHandle struct {
	coreID string
	file   media.VirtualFile
}

var registry = struct {
	mu       sync.Mutex
	sessions map[string]*session
	files    map[string]*fileHandle
	virtuals map[string]*virtualHandle
}{
	sessions: map[string]*session{},
	files:    map[string]*fileHandle{},
	virtuals: map[string]*virtualHandle{},
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  any             `json:"data,omitempty"`
	Error *core.ErrorInfo `json:"error,omitempty"`
}

func Open(configPath, workDir string) (string, error) {
	c, err := core.Open(context.Background(), core.Options{
		ConfigPath: configPath,
		WorkDir:    workDir,
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

func OpenJSON(configPath, workDir string) string {
	id, err := Open(configPath, workDir)
	return resultJSON(id, err)
}

func ImportConfigJSON(srcPath, workDir string) string {
	path, err := core.ImportConfig(srcPath, workDir)
	return resultJSON(path, err)
}

func OpenImportedJSON(workDir string) string {
	c, err := core.OpenImported(context.Background(), workDir)
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

func List(coreID, path string) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	entries, err := s.core.List(context.Background(), path)
	if err != nil {
		return "", wrapError(err)
	}
	out := make([]entry, 0, len(entries))
	for _, item := range entries {
		out = append(out, fromDriveEntry(item, core.JoinPath(path, item.Name)))
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", wrapError(err)
	}
	return string(data), nil
}

func ListJSON(coreID, path string) string {
	raw, err := List(coreID, path)
	if err != nil {
		return resultJSON(nil, err)
	}
	var entries []entry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return resultJSON(nil, err)
	}
	return resultJSON(entries, nil)
}

func Stat(coreID, path string) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	item, err := s.core.Stat(context.Background(), path)
	if err != nil {
		return "", wrapError(err)
	}
	data, err := json.Marshal(fromDriveEntry(item, path))
	if err != nil {
		return "", wrapError(err)
	}
	return string(data), nil
}

func StatJSON(coreID, path string) string {
	raw, err := Stat(coreID, path)
	if err != nil {
		return resultJSON(nil, err)
	}
	var item entry
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return resultJSON(nil, err)
	}
	return resultJSON(item, nil)
}

func MkdirJSON(coreID, path string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	item, err := s.core.Mkdir(ctx, path)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(fromDriveEntry(item, path), nil)
}

func RenameJSON(coreID, oldPath, newPath string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	return resultJSON(nil, s.core.Rename(ctx, oldPath, newPath))
}

func RemoveJSON(coreID, path string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	return resultJSON(nil, s.core.Remove(ctx, path))
}

func CapabilitiesJSON(coreID, path string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	info, err := s.core.Capabilities(ctx, path)
	return resultJSON(info, err)
}

func UploadLocalFileJSON(coreID, localPath, remotePath string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	item, err := s.core.UploadLocalFile(ctx, localPath, remotePath)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(fromDriveEntry(item, remotePath), nil)
}

func FileInfoJSON(coreID, path string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	info, err := s.core.FileInfo(context.Background(), path)
	return resultJSON(info, err)
}

func ValidateResumeJSON(coreID, path, id string, size int64, modTime string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	check, err := s.core.ValidateResume(context.Background(), path, id, size, modTime)
	return resultJSON(check, err)
}

func OpenFile(coreID, path string) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	item, err := s.core.Stat(context.Background(), path)
	if err != nil {
		return "", wrapError(err)
	}
	if item.IsDir {
		return "", wrapError(fmt.Errorf("mobile: %s is a directory", path))
	}
	id, err := newID()
	if err != nil {
		return "", wrapError(err)
	}
	registry.mu.Lock()
	registry.files[id] = &fileHandle{coreID: coreID, path: path, size: item.Size}
	registry.mu.Unlock()
	return id, nil
}

func OpenFileJSON(coreID, path string) string {
	id, err := OpenFile(coreID, path)
	return resultJSON(id, err)
}

func ReadAt(handleID string, offset int64, length int) ([]byte, error) {
	if offset < 0 {
		return nil, wrapError(fmt.Errorf("mobile: offset must be non-negative"))
	}
	if length < 0 {
		return nil, wrapError(fmt.Errorf("mobile: length must be non-negative"))
	}
	if length == 0 {
		return []byte{}, nil
	}
	handle, err := getFile(handleID)
	if err != nil {
		return nil, wrapError(err)
	}
	s, err := getSession(handle.coreID)
	if err != nil {
		return nil, wrapError(err)
	}
	data, err := s.core.ReadAt(context.Background(), handle.path, offset, length, 0)
	if err != nil {
		return nil, wrapError(err)
	}
	return data, nil
}

func ReadAtWithTimeout(handleID string, offset int64, length int, timeoutMS int) ([]byte, error) {
	handle, err := getFile(handleID)
	if err != nil {
		return nil, wrapError(err)
	}
	s, err := getSession(handle.coreID)
	if err != nil {
		return nil, wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	data, err := s.core.ReadAt(ctx, handle.path, offset, length, 0)
	if err != nil {
		return nil, wrapError(err)
	}
	return data, nil
}

func ReadAtJSON(handleID string, offset int64, length int, timeoutMS int) string {
	data, err := ReadAtWithTimeout(handleID, offset, length, timeoutMS)
	return resultJSON(data, err)
}

func ProbeMP4JSON(coreID, path string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	probe, err := s.core.ProbeMP4(ctx, path)
	return resultJSON(probe, err)
}

type virtualOpenResult struct {
	Handle string                `json:"handle"`
	Info   media.VirtualFileInfo `json:"info"`
}

func OpenVirtualFile(coreID, path, mode string, timeoutMS int) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	file, err := s.core.OpenVirtualFile(ctx, path, mode)
	if err != nil {
		return "", wrapError(err)
	}
	id, err := newID()
	if err != nil {
		_ = file.Close()
		return "", wrapError(err)
	}
	registry.mu.Lock()
	registry.virtuals[id] = &virtualHandle{coreID: coreID, file: file}
	registry.mu.Unlock()
	data, err := json.Marshal(virtualOpenResult{Handle: id, Info: file.Info()})
	if err != nil {
		_ = CloseVirtualFile(id)
		return "", wrapError(err)
	}
	return string(data), nil
}

func OpenVirtualFileJSON(coreID, path, mode string, timeoutMS int) string {
	raw, err := OpenVirtualFile(coreID, path, mode, timeoutMS)
	if err != nil {
		return resultJSON(nil, err)
	}
	var data virtualOpenResult
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return resultJSON(nil, err)
	}
	return resultJSON(data, nil)
}

func ReadVirtualFileAt(handleID string, offset int64, length int, timeoutMS int) ([]byte, error) {
	handle, err := getVirtualFile(handleID)
	if err != nil {
		return nil, wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	data, err := handle.file.ReadAt(ctx, offset, length)
	if err != nil {
		return nil, wrapError(err)
	}
	return data, nil
}

func ReadVirtualFileAtJSON(handleID string, offset int64, length int, timeoutMS int) string {
	data, err := ReadVirtualFileAt(handleID, offset, length, timeoutMS)
	return resultJSON(data, err)
}

func CloseVirtualFile(handleID string) error {
	registry.mu.Lock()
	handle, ok := registry.virtuals[handleID]
	if ok {
		delete(registry.virtuals, handleID)
	}
	registry.mu.Unlock()
	if !ok {
		return wrapError(fmt.Errorf("mobile: unknown virtual file handle %q", handleID))
	}
	return wrapError(handle.file.Close())
}

func CloseVirtualFileJSON(handleID string) string {
	return resultJSON(nil, CloseVirtualFile(handleID))
}

func CloseFile(handleID string) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, ok := registry.files[handleID]; !ok {
		return wrapError(fmt.Errorf("mobile: unknown file handle %q", handleID))
	}
	delete(registry.files, handleID)
	return nil
}

func CloseFileJSON(handleID string) string {
	return resultJSON(nil, CloseFile(handleID))
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

func DebugSnapshotJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	raw, err := s.core.DebugSnapshotJSON(context.Background())
	return rawResultJSON(raw, err)
}

func FlushReadCacheJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, s.core.FlushReadCache())
}

func StartDebugServerJSON(coreID, listen string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, s.core.StartDebugServer(context.Background(), listen))
}

func StopDebugServerJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, s.core.StopDebugServer(context.Background()))
}

func LogFilesJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	files, err := s.core.LogFiles()
	return resultJSON(files, err)
}

func ReadLogJSON(coreID, name string, offset int64, length int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	data, err := s.core.ReadLog(name, offset, length)
	return resultJSON(data, err)
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

func fromDriveEntry(item drive.Entry, path string) entry {
	out := entry{
		Name:     item.Name,
		Path:     path,
		ID:       item.ID,
		ParentID: item.ParentID,
		IsDir:    item.IsDir,
		Size:     item.Size,
	}
	if !item.ModTime.IsZero() {
		out.ModTime = item.ModTime.Format(time.RFC3339)
	}
	return out
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func resultJSON(data any, err error) string {
	env := envelope{OK: err == nil, Data: data}
	if err != nil {
		info := core.ClassifyError(err)
		env.Error = &info
	}
	raw, marshalErr := json.Marshal(env)
	if marshalErr != nil {
		fallback := core.ClassifyError(marshalErr)
		raw, _ = json.Marshal(envelope{OK: false, Error: &fallback})
	}
	return string(raw)
}

func rawResultJSON(raw string, err error) string {
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var data any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return resultJSON(nil, err)
	}
	return resultJSON(data, nil)
}

type classifiedError struct {
	info core.ErrorInfo
}

func (e classifiedError) Error() string {
	if e.info.Code == "" {
		return e.info.Message
	}
	return string(e.info.Code) + ": " + e.info.Message
}

func wrapError(err error) error {
	if err == nil {
		return nil
	}
	return classifiedError{info: core.ClassifyError(err)}
}
