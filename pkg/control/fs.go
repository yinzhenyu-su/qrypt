package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// FSEntry is the transport view of one path served by the Core filesystem.
// Only fields that the CLI prints or matches on are carried; backend-private
// data (drive.Entry.Extra) is intentionally left behind.
type FSEntry struct {
	ID       string    `json:"id,omitempty"`
	ParentID string    `json:"parent_id,omitempty"`
	Name     string    `json:"name"`
	Path     string    `json:"path,omitempty"`
	IsDir    bool      `json:"is_dir"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"mod_time,omitempty"`
	// RemoteName carries the backend name when it differs from Name (encrypted
	// mounts). It is only serialized for the list/stat responses; writes never
	// send it because the owner resolves the path itself.
	RemoteName string `json:"remote_name,omitempty"`
}

// FSCopyRequest is a direct backend-to-backend copy request.
type FSCopyRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Recursive   bool   `json:"recursive"`
	Force       bool   `json:"force"`
}

// FSService is the minimal data plane over the command endpoint: the subset of
// vfs.FileSystem that lets the CLI list, stat, upload and rename through the
// single owner Core instead of opening its own VFS. It stays separate from the
// diagnostic surfaces so a read-only inspector never gains write authority.
type FSService interface {
	Stat(ctx context.Context, path string) (FSEntry, error)
	List(ctx context.Context, path string) ([]FSEntry, error)
	// ReadStream streams file bytes so large files never have to be buffered
	// by the server or the client. raw requests backend bytes without
	// decryption or read-cache transforms.
	ReadStream(ctx context.Context, path string, offset, size int64, raw bool) (io.ReadCloser, error)
	Create(ctx context.Context, path string) error
	WriteAt(ctx context.Context, path string, offset int64, data []byte) (int, error)
	Flush(ctx context.Context, path string) error
	Mkdir(ctx context.Context, path string) (FSEntry, error)
	Remove(ctx context.Context, path string, dir bool) error
	Rename(ctx context.Context, source, destination string) error
	Truncate(ctx context.Context, path string, size int64) error
	// WaitPath blocks until this path has no pending upload/delete work, so a
	// client can keep the "wait for the upload to finish" contract without
	// waiting for the whole shared runtime to go idle.
	WaitPath(ctx context.Context, path string, timeout time.Duration) error
	// WaitIdle blocks until the whole owner filesystem has no pending uploads
	// or active tasks; it preserves the legacy "wait for everything" contract.
	WaitIdle(ctx context.Context, timeout time.Duration) error
	// InvalidateList drops persisted and in-memory listings for path so the
	// next list reflects the backend (used by `fs list --refresh`).
	InvalidateList(ctx context.Context, path string) error
	// Space reports aggregate capacity for the owner filesystem.
	Space(ctx context.Context) (FSSpace, error)
	// MountSpaces reports per-mount capacity breakdown.
	MountSpaces(ctx context.Context) ([]FSMountSpace, error)
	// PendingUploads lists uploads staged but not yet committed.
	PendingUploads(ctx context.Context) ([]vfs.PendingUpload, error)
	// Copy runs a direct backend-to-backend copy and returns the driver_copy
	// result document.
	Copy(ctx context.Context, req FSCopyRequest) (json.RawMessage, error)
	// Resolve reports the backend identity (remote name) of one namespace path
	// for diagnostics-style clients such as `fs list --remote-names`.
	Resolve(ctx context.Context, path string) (FSResolveResult, error)
}

// FSResolveResult is the backend identity of one namespace path.
type FSResolveResult struct {
	Path       string `json:"path"`
	PlainName  string `json:"plain_name,omitempty"`
	RemoteName string `json:"remote_name,omitempty"`
	IsDir      bool   `json:"is_dir,omitempty"`
}

// FSSpace is the transport view of drive.Space.
type FSSpace struct {
	Total int64 `json:"total"`
	Free  int64 `json:"free"`
}

// FSMountSpace is one mount's capacity breakdown.
type FSMountSpace struct {
	Name  string `json:"name"`
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
	Error string `json:"error,omitempty"`
}

// SetFSService installs the data-plane service before Start.
func (s *CommandServer) SetFSService(service FSService) {
	s.fs = service
}

func (s *CommandServer) handleFS(w http.ResponseWriter, r *http.Request) {
	if s.fs == nil {
		writeUnavailable(w, "filesystem service unavailable")
		return
	}
	op := strings.TrimPrefix(r.URL.Path, "/v1/fs/")
	if isFSMutation(op) {
		s.handleFSMutation(w, r, op)
		return
	}
	s.handleFSOperation(w, r, op)
}

// isFSMutation reports whether op writes through the owner filesystem and thus
// takes an optional idempotency key.
func isFSMutation(op string) bool {
	switch op {
	case "create", "write", "flush", "mkdir", "remove", "rename", "truncate", "copy":
		return true
	}
	return false
}

// handleFSMutation executes a mutation at most once per operation id and
// replays the recorded response for a repeated id.
func (s *CommandServer) handleFSMutation(w http.ResponseWriter, r *http.Request, op string) {
	id := strings.TrimSpace(r.Header.Get(OperationIDHeader))
	if id == "" || s.operations == nil {
		s.handleFSOperation(w, r, op)
		return
	}
	if entry, ok := s.operations.lookup(id); ok {
		for key, values := range entry.header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(entry.status)
		_, _ = w.Write(entry.payload)
		return
	}
	recorder := newBufferedResponseWriter()
	s.handleFSOperation(recorder, r, op)
	if recorder.status >= 200 && recorder.status < 300 {
		s.operations.store(id, recorder.status, recorder.header, recorder.body.Bytes())
	}
	recorder.flushTo(w)
}

func (s *CommandServer) handleFSOperation(w http.ResponseWriter, r *http.Request, op string) {
	switch op {
	case "stat":
		s.handleFSStat(w, r)
	case "list":
		s.handleFSList(w, r)
	case "read":
		s.handleFSRead(w, r)
	case "create":
		s.handleFSMutations(w, r, func(ctx context.Context, req fsPathRequest) error {
			return s.fs.Create(ctx, req.Path)
		})
	case "flush":
		s.handleFSMutations(w, r, func(ctx context.Context, req fsPathRequest) error {
			return s.fs.Flush(ctx, req.Path)
		})
	case "remove":
		s.handleFSRemove(w, r)
	case "write":
		s.handleFSWrite(w, r)
	case "mkdir":
		s.handleFSMkdir(w, r)
	case "rename":
		s.handleFSRename(w, r)
	case "truncate":
		s.handleFSTruncate(w, r)
	case "invalidate":
		s.handleFSInvalidate(w, r)
	case "copy":
		s.handleFSCopy(w, r)
	case "space":
		s.handleFSSpace(w, r)
	case "spaces":
		s.handleFSMountSpaces(w, r)
	case "pending":
		s.handleFSPendingUploads(w, r)
	case "wait":
		s.handleFSWait(w, r)
	case "idle":
		s.handleFSIdle(w, r)
	case "resolve":
		s.handleFSResolve(w, r)
	default:
		writeNotFound(w, "unknown filesystem operation")
	}
}

func fsQueryPath(r *http.Request) (string, error) {
	value := r.URL.Query().Get("path")
	if value == "" {
		return "", errors.New("missing path")
	}
	if decoded, err := url.PathUnescape(value); err == nil {
		value = decoded
	}
	return value, nil
}

func (s *CommandServer) handleFSStat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	path, err := fsQueryPath(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	entry, err := s.fs.Stat(r.Context(), path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, entry)
}

func (s *CommandServer) handleFSCopy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	var req FSCopyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	result, err := s.fs.Copy(r.Context(), req)
	if err != nil {
		writeFSError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(result)
}

func (s *CommandServer) handleFSList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	path, err := fsQueryPath(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	entries, err := s.fs.List(r.Context(), path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	if entries == nil {
		entries = []FSEntry{}
	}
	writeJSON(w, map[string]any{"path": path, "entries": entries})
}

func (s *CommandServer) handleFSRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	path, err := fsQueryPath(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	size, _ := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
	raw := r.URL.Query().Get("raw") == "1"
	stream, err := s.fs.ReadStream(r.Context(), path, offset, size, raw)
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, stream)
}

type fsPathRequest struct {
	Path string `json:"path"`
}

type fsRemoveRequest struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir,omitempty"`
}

type fsRenameRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type fsTruncateRequest struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func writeFSRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(value); err != nil {
		writeBadRequest(w, "invalid request: "+err.Error())
		return false
	}
	return true
}

func (s *CommandServer) handleFSMutations(w http.ResponseWriter, r *http.Request, apply func(context.Context, fsPathRequest) error) {
	var req fsPathRequest
	if !writeFSRequest(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeBadRequest(w, "missing path")
		return
	}
	if err := apply(r.Context(), req); err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, map[string]any{"path": req.Path, "ok": true})
}

func (s *CommandServer) handleFSWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	path, err := fsQueryPath(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	data, err := io.ReadAll(io.LimitReader(r.Body, MaxWriteBytes))
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	written, err := s.fs.WriteAt(r.Context(), path, offset, data)
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, map[string]any{"written": written})
}

func (s *CommandServer) handleFSRemove(w http.ResponseWriter, r *http.Request) {
	var req fsRemoveRequest
	if !writeFSRequest(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeBadRequest(w, "missing path")
		return
	}
	if err := s.fs.Remove(r.Context(), req.Path, req.Dir); err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, map[string]any{"path": req.Path, "ok": true})
}

func (s *CommandServer) handleFSRename(w http.ResponseWriter, r *http.Request) {
	var req fsRenameRequest
	if !writeFSRequest(w, r, &req) {
		return
	}
	if req.Source == "" || req.Destination == "" {
		writeBadRequest(w, "missing source or destination")
		return
	}
	if err := s.fs.Rename(r.Context(), req.Source, req.Destination); err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, map[string]any{"source": req.Source, "destination": req.Destination, "ok": true})
}

func (s *CommandServer) handleFSTruncate(w http.ResponseWriter, r *http.Request) {
	var req fsTruncateRequest
	if !writeFSRequest(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeBadRequest(w, "missing path")
		return
	}
	if err := s.fs.Truncate(r.Context(), req.Path, req.Size); err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, map[string]any{"path": req.Path, "ok": true})
}

func (s *CommandServer) handleFSMkdir(w http.ResponseWriter, r *http.Request) {
	var req fsPathRequest
	if !writeFSRequest(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeBadRequest(w, "missing path")
		return
	}
	entry, err := s.fs.Mkdir(r.Context(), req.Path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, entry)
}

func (s *CommandServer) handleFSInvalidate(w http.ResponseWriter, r *http.Request) {
	var req fsPathRequest
	if !writeFSRequest(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeBadRequest(w, "missing path")
		return
	}
	if err := s.fs.InvalidateList(r.Context(), req.Path); err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, map[string]any{"invalidated": req.Path})
}

func (s *CommandServer) handleFSWait(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path      string `json:"path"`
		TimeoutMS int64  `json:"timeout_ms"`
	}
	if !writeFSRequest(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeBadRequest(w, "missing path")
		return
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if err := s.fs.WaitPath(r.Context(), req.Path, timeout); err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, map[string]any{"path": req.Path, "settled": true})
}

func (s *CommandServer) handleFSResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if s.fs == nil {
		writeUnavailable(w, "fs service unavailable")
		return
	}
	path, err := fsQueryPath(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	info, err := s.fs.Resolve(r.Context(), path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, info)
}

func (s *CommandServer) handleFSIdle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TimeoutMS int64 `json:"timeout_ms"`
	}
	if !writeFSRequest(w, r, &req) {
		return
	}
	if err := s.fs.WaitIdle(r.Context(), time.Duration(req.TimeoutMS)*time.Millisecond); err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, map[string]any{"idle": true})
}

func (s *CommandServer) handleFSSpace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	space, err := s.fs.Space(r.Context())
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, space)
}

func (s *CommandServer) handleFSMountSpaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	mounts, err := s.fs.MountSpaces(r.Context())
	if err != nil {
		writeFSError(w, err)
		return
	}
	if mounts == nil {
		mounts = []FSMountSpace{}
	}
	writeJSON(w, map[string]any{"mounts": mounts})
}

func (s *CommandServer) handleFSPendingUploads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	pending, err := s.fs.PendingUploads(r.Context())
	if err != nil {
		writeFSError(w, err)
		return
	}
	if pending == nil {
		pending = []vfs.PendingUpload{}
	}
	writeJSON(w, map[string]any{"pending": pending})
}

// --- client side -----------------------------------------------------------

// FSStat returns metadata for one Core path.
func (c *Client) FSStat(ctx context.Context, path string) (FSEntry, error) {
	body, err := c.Get(ctx, "/v1/fs/stat?path="+url.QueryEscape(path))
	if err != nil {
		return FSEntry{}, err
	}
	var entry FSEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		return FSEntry{}, fmt.Errorf("control: decode fs stat: %w", err)
	}
	return entry, nil
}

// FSList lists one Core directory.
func (c *Client) FSList(ctx context.Context, path string) ([]FSEntry, error) {
	body, err := c.Get(ctx, "/v1/fs/list?path="+url.QueryEscape(path))
	if err != nil {
		return nil, err
	}
	var result struct {
		Entries []FSEntry `json:"entries"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("control: decode fs list: %w", err)
	}
	return result.Entries, nil
}

// FSReadStream opens a streaming read of a Core file range. A size of zero
// means "until EOF"; raw requests backend bytes without decryption.
func (c *Client) FSReadStream(ctx context.Context, path string, offset, size int64, raw bool) (io.ReadCloser, error) {
	query := url.Values{}
	query.Set("path", path)
	query.Set("offset", strconv.FormatInt(offset, 10))
	query.Set("size", strconv.FormatInt(size, 10))
	if raw {
		query.Set("raw", "1")
	}
	return c.OpenStream(ctx, "/v1/fs/read?"+query.Encode())
}

// FSRead reads a bounded range of a Core file into memory.
func (c *Client) FSRead(ctx context.Context, path string, offset, size int64) ([]byte, error) {
	stream, err := c.FSReadStream(ctx, path, offset, size, false)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return io.ReadAll(stream)
}

// FSSpace reports aggregate capacity of the Core filesystem.
func (c *Client) FSSpace(ctx context.Context) (FSSpace, error) {
	body, err := c.Get(ctx, "/v1/fs/space")
	if err != nil {
		return FSSpace{}, err
	}
	var space FSSpace
	if err := json.Unmarshal(body, &space); err != nil {
		return FSSpace{}, fmt.Errorf("control: decode fs space: %w", err)
	}
	return space, nil
}

// FSMountSpaces reports per-mount capacity breakdown.
func (c *Client) FSMountSpaces(ctx context.Context) ([]FSMountSpace, error) {
	body, err := c.Get(ctx, "/v1/fs/spaces")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Mounts []FSMountSpace `json:"mounts"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("control: decode fs spaces: %w", err)
	}
	return payload.Mounts, nil
}

// FSPendingUploads lists uploads staged but not yet committed.
func (c *Client) FSPendingUploads(ctx context.Context) ([]vfs.PendingUpload, error) {
	body, err := c.Get(ctx, "/v1/fs/pending")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Pending []vfs.PendingUpload `json:"pending"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("control: decode fs pending: %w", err)
	}
	return payload.Pending, nil
}

// FSCopy runs a direct backend-to-backend copy on the Core and returns the
// driver_copy result document.
func (c *Client) FSCopy(ctx context.Context, req FSCopyRequest) (json.RawMessage, error) {
	return c.PostJSON(ctx, "/v1/fs/copy", req)
}

// FSCreate opens (or truncates) a file on the Core.
func (c *Client) FSCreate(ctx context.Context, path string) error {
	return c.fsPost(ctx, "/v1/fs/create", fsPathRequest{Path: path})
}

// FSWriteAt writes data at offset on the Core.
func (c *Client) FSWriteAt(ctx context.Context, path string, offset int64, data []byte) (int, error) {
	body, err := c.PostBytes(ctx, "/v1/fs/write?path="+url.QueryEscape(path)+"&offset="+strconv.FormatInt(offset, 10), data)
	if err != nil {
		return 0, err
	}
	var result struct {
		Written int `json:"written"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("control: decode fs write: %w", err)
	}
	return result.Written, nil
}

// FSFlush commits the pending bytes for path.
func (c *Client) FSFlush(ctx context.Context, path string) error {
	return c.fsPost(ctx, "/v1/fs/flush", fsPathRequest{Path: path})
}

// FSMkdir creates a directory on the Core.
func (c *Client) FSMkdir(ctx context.Context, path string) (FSEntry, error) {
	body, err := c.PostJSON(ctx, "/v1/fs/mkdir", fsPathRequest{Path: path})
	if err != nil {
		return FSEntry{}, err
	}
	var entry FSEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		return FSEntry{}, fmt.Errorf("control: decode fs mkdir: %w", err)
	}
	return entry, nil
}

// FSRemove removes a file or directory on the Core.
func (c *Client) FSRemove(ctx context.Context, path string, dir bool) error {
	return c.fsPost(ctx, "/v1/fs/remove", fsRemoveRequest{Path: path, Dir: dir})
}

// FSRename renames a path on the Core.
func (c *Client) FSRename(ctx context.Context, source, destination string) error {
	return c.fsPost(ctx, "/v1/fs/rename", fsRenameRequest{Source: source, Destination: destination})
}

// FSTruncate resizes a file on the Core.
func (c *Client) FSTruncate(ctx context.Context, path string, size int64) error {
	return c.fsPost(ctx, "/v1/fs/truncate", fsTruncateRequest{Path: path, Size: size})
}

// FSWaitPath waits until the Core reports no pending work for path.
// FSInvalidateList drops cached directory listings for path.
func (c *Client) FSInvalidateList(ctx context.Context, path string) error {
	_, err := c.PostJSON(ctx, "/v1/fs/invalidate", fsPathRequest{Path: path})
	return err
}

func (c *Client) FSWaitPath(ctx context.Context, path string, timeout time.Duration) error {
	return c.fsPost(ctx, "/v1/fs/wait", map[string]any{"path": path, "timeout_ms": timeout.Milliseconds()})
}

// FSWaitIdle waits until the whole owner filesystem settles.
func (c *Client) FSWaitIdle(ctx context.Context, timeout time.Duration) error {
	return c.fsPost(ctx, "/v1/fs/idle", map[string]any{"timeout_ms": timeout.Milliseconds()})
}

func (c *Client) fsPost(ctx context.Context, path string, value any) error {
	_, err := c.PostJSON(ctx, path, value)
	return err
}

// PostBytes sends a raw body to a mutable control endpoint.
func (c *Client) PostBytes(ctx context.Context, path string, data []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	return c.do(req, path)
}
