// Package httpapi serves the resident Core HTTP API: read-only v1 endpoints
// plus the v2 write endpoints (upload, mkdir, remove, rename, copy, truncate).
//
// The gateway is a separate process that talks to the Core over its local
// control endpoint; the Core itself never listens on TCP. Keeping the
// translation in one place means the HTTP surface shares the Core's VFS,
// overlays and caches with `fs` and FUSE, so identical operations observe the
// same state.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// Core is the subset of the Core control client the gateway uses. It is an
// interface so handler tests can run without a real Core.
type Core interface {
	RuntimeStatus(ctx context.Context) (control.RuntimeStatus, error)
	ListMounts(ctx context.Context) ([]control.MountStatus, error)
	FSStat(ctx context.Context, path string) (control.FSEntry, error)
	FSList(ctx context.Context, path string) ([]control.FSEntry, error)
	FSReadStream(ctx context.Context, path string, offset, size int64, raw bool) (io.ReadCloser, error)
	FSPendingUploads(ctx context.Context) ([]vfs.PendingUpload, error)
	FSCreate(ctx context.Context, path string) error
	FSWriteAt(ctx context.Context, path string, offset int64, data []byte) (int, error)
	FSFlush(ctx context.Context, path string) error
	FSMkdir(ctx context.Context, path string) (control.FSEntry, error)
	FSRemove(ctx context.Context, path string, dir bool) error
	FSRename(ctx context.Context, source, destination string) error
	FSCopy(ctx context.Context, req control.FSCopyRequest) (json.RawMessage, error)
	FSTruncate(ctx context.Context, path string, size int64) error
	FSWaitPath(ctx context.Context, path string, timeout time.Duration) error
}

// Options configures one gateway listener.
type Options struct {
	// Token authenticates every request except /v1/health. It is required.
	Token string
	// AllowOrigins is the CORS/CSRF allowlist. Empty means loopback origins
	// only.
	AllowOrigins []string
	// AllowedHosts are extra Host header values accepted for non-loopback
	// listeners (for example a reverse-proxy hostname).
	AllowedHosts []string
	// RequestsPerSecond and Burst bound the global request rate. Zero picks
	// defaults.
	RequestsPerSecond float64
	Burst             int
	// MaxConcurrentReads bounds simultaneously open file streams.
	MaxConcurrentReads int
	// EventInterval is the SSE heartbeat period.
	EventInterval time.Duration
	// MaxUploadBytes bounds one upload request body. Zero picks 64 MiB.
	MaxUploadBytes int64
}

func (o Options) withDefaults() Options {
	if o.RequestsPerSecond <= 0 {
		o.RequestsPerSecond = 50
	}
	if o.Burst <= 0 {
		o.Burst = 100
	}
	if o.MaxConcurrentReads <= 0 {
		o.MaxConcurrentReads = 32
	}
	if o.EventInterval <= 0 {
		o.EventInterval = 15 * time.Second
	}
	if o.MaxUploadBytes <= 0 {
		o.MaxUploadBytes = 64 << 20
	}
	return o
}

// Server is an http.Handler plus the small amount of shared state the handlers
// need (rate limiter, stream slots, options).
type Server struct {
	core    Core
	opts    Options
	limiter *rate.Limiter
	streams chan struct{}
	now     func() time.Time
}

// New builds a gateway for one Core.
func New(core Core, opts Options) *Server {
	opts = opts.withDefaults()
	return &Server{
		core:    core,
		opts:    opts,
		limiter: rate.NewLimiter(rate.Limit(opts.RequestsPerSecond), opts.Burst),
		streams: make(chan struct{}, opts.MaxConcurrentReads),
		now:     time.Now,
	}
}

// Handler returns the full middleware-wrapped handler tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/health", s.public(http.HandlerFunc(s.handleHealth)))
	mux.Handle("/v1/session", s.private(http.HandlerFunc(s.handleSession)))
	mux.Handle("/v1/mounts", s.private(http.HandlerFunc(s.handleMounts)))
	mux.Handle("/v1/tasks", s.private(http.HandlerFunc(s.handleTasks)))
	mux.Handle("/v1/events", s.private(http.HandlerFunc(s.handleEvents)))
	mux.Handle("/v1/fs/read", s.private(http.HandlerFunc(s.handleRead)))
	mux.Handle("/v1/fs/download", s.private(http.HandlerFunc(s.handleDownload)))
	mux.Handle("/v1/fs/", s.private(http.HandlerFunc(s.handleFS)))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint", false)
	}))
	return s.common(mux)
}

// --- middleware ---------------------------------------------------------

func (s *Server) common(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				logging.L.Errorf("httpapi: panic serving %s %s: %v", r.Method, r.URL.Path, v)
				writeError(w, http.StatusInternalServerError, "internal", "internal error", false)
			}
		}()
		if !s.hostAllowed(r.Host) {
			writeError(w, http.StatusForbidden, "forbidden", "unexpected Host header", false)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && !s.originAllowed(origin) {
			writeError(w, http.StatusForbidden, "forbidden", "origin not allowed", false)
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Range, If-Range, If-None-Match, X-Qrypt-Operation-Id, X-Qrypt-CSRF-Token")
			w.Header().Set("Access-Control-Expose-Headers", "ETag, Content-Range, Content-Length, Accept-Ranges")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Allow", "GET, HEAD, POST, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !s.limiter.Allow() {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests", true)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) public(next http.Handler) http.Handler {
	return next
}

func (s *Server) private(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="qrypt"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token", false)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.opts.Token)) == 1
}

func (s *Server) hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	for _, allowed := range s.opts.AllowedHosts {
		if strings.EqualFold(host, allowed) {
			return true
		}
	}
	return false
}

func (s *Server) originAllowed(origin string) bool {
	for _, allowed := range s.opts.AllowOrigins {
		if origin == allowed {
			return true
		}
	}
	if len(s.opts.AllowOrigins) > 0 {
		return false
	}
	// With no explicit allowlist only same-machine web origins are accepted.
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) acquireStream() bool {
	select {
	case s.streams <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) releaseStream() { <-s.streams }

// --- handlers -----------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	status, err := s.core.RuntimeStatus(ctx)
	if err != nil {
		writeCoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"pid":     status.PID,
		"version": status.Version,
	})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	status, err := s.core.RuntimeStatus(ctx)
	if err != nil {
		writeCoreError(w, err)
		return
	}
	mounts, err := s.core.ListMounts(ctx)
	if err != nil {
		writeCoreError(w, err)
		return
	}
	names := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		names = append(names, mount.Name)
	}
	writeData(w, http.StatusOK, map[string]any{
		"pid":            status.PID,
		"version":        status.Version,
		"config_path":    status.ConfigPath,
		"work_dir":       status.WorkDir,
		"started_at":     status.StartedAt,
		"uptime_seconds": status.UptimeSeconds,
		"busy":           status.Busy,
		"mount_names":    names,
	})
}

func (s *Server) handleMounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET", false)
		return
	}
	mounts, err := s.core.ListMounts(r.Context())
	if err != nil {
		writeCoreError(w, err)
		return
	}
	if mounts == nil {
		mounts = []control.MountStatus{}
	}
	writeData(w, http.StatusOK, map[string]any{"mounts": mounts})
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET", false)
		return
	}
	pending, err := s.core.FSPendingUploads(r.Context())
	if err != nil {
		writeCoreError(w, err)
		return
	}
	if pending == nil {
		pending = []vfs.PendingUpload{}
	}
	writeData(w, http.StatusOK, map[string]any{"pending": pending})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET", false)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported", false)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sendStatus := func() bool {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		status, err := s.core.RuntimeStatus(ctx)
		payload := map[string]any{"ok": err == nil}
		if err == nil {
			payload["pid"] = status.PID
			payload["busy"] = status.Busy
			payload["uptime_seconds"] = status.UptimeSeconds
		}
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return false
		}
		if _, writeErr := fmt.Fprintf(w, "event: status\ndata: %s\n\n", encoded); writeErr != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !sendStatus() {
		return
	}
	ticker := time.NewTicker(s.opts.EventInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !sendStatus() {
				return
			}
		}
	}
}

func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(r.URL.Path, "/v1/fs/")
	switch op {
	case "list":
		s.handleList(w, r)
	case "stat":
		s.handleStat(w, r)
	case "upload":
		s.handleUpload(w, r)
	case "upload/status":
		s.handleUploadStatus(w, r)
	case "mkdir":
		s.handleMkdir(w, r)
	case "remove":
		s.handleRemove(w, r)
	case "rename":
		s.handleRename(w, r)
	case "copy":
		s.handleCopy(w, r)
	case "truncate":
		s.handleTruncate(w, r)
	case "":
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint", false)
	default:
		// create/write/flush/invalidate stay internal to the Core data plane:
		// clients upload through /v1/fs/upload so offset accounting and path
		// waiting happen in one place.
		if op == "create" || op == "write" || op == "flush" || op == "invalidate" {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
				"use /v1/fs/upload for writes", false)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint", false)
	}
}

// --- write endpoints (v2) ----------------------------------------------

// mutationContext forwards the client's idempotency key to the Core so a
// retried write is replayed instead of applied twice.
func (s *Server) mutationContext(r *http.Request) context.Context {
	if id := strings.TrimSpace(r.Header.Get("X-Qrypt-Operation-Id")); id != "" {
		return control.WithOperationID(r.Context(), id)
	}
	return r.Context()
}

// csrfAllowed protects cookie-carrying requests. Bearer tokens are not sent
// automatically by browsers, so a request without cookies is already
// CSRF-safe; one with cookies must prove it can read the token.
func (s *Server) csrfAllowed(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Cookie") == "" {
		return true
	}
	got := r.Header.Get("X-Qrypt-CSRF-Token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.Token)) != 1 {
		writeError(w, http.StatusForbidden, "forbidden", "missing or invalid CSRF token", false)
		return false
	}
	return true
}

func decodeWriteBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST", false)
		return false
	}
	defer r.Body.Close()
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error(), false)
		return false
	}
	return true
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST", false)
		return
	}
	if !s.csrfAllowed(w, r) {
		return
	}
	requestPath, err := normalizePath(r.URL.Query().Get("path"))
	if err != nil || requestPath == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path query is required", false)
		return
	}
	offset := int64(0)
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "offset must be a non-negative integer", false)
			return
		}
	}
	wait, _ := strconv.ParseBool(r.URL.Query().Get("wait"))
	ctx := s.mutationContext(r)
	if offset == 0 {
		if err := s.core.FSCreate(ctx, requestPath); err != nil {
			writeCoreError(w, err)
			return
		}
	} else if _, err := s.core.FSStat(ctx, requestPath); err != nil {
		writeCoreError(w, err)
		return
	}

	body := http.MaxBytesReader(w, r.Body, s.opts.MaxUploadBytes)
	defer body.Close()
	buf := make([]byte, 256<<10)
	written := offset
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, err := s.core.FSWriteAt(ctx, requestPath, written, buf[:n]); err != nil {
				writeCoreError(w, err)
				return
			}
			written += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			var maxErr *http.MaxBytesError
			if errors.As(readErr, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "too_large", "upload body exceeds the server limit", false)
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_request", readErr.Error(), false)
			return
		}
	}
	if err := s.core.FSFlush(ctx, requestPath); err != nil {
		writeCoreError(w, err)
		return
	}
	if wait {
		if err := s.core.FSWaitPath(ctx, requestPath, 30*time.Second); err != nil {
			writeCoreError(w, err)
			return
		}
	}
	pending, _ := s.pendingFor(r.Context(), requestPath)
	writeData(w, http.StatusOK, map[string]any{
		"path":    requestPath,
		"offset":  written,
		"pending": pending,
		"task": map[string]any{
			"id":      requestPath,
			"path":    requestPath,
			"bytes":   written,
			"pending": pending,
		},
	})
}

// handleUploadStatus reports how much of a path has been accepted and whether
// an upload task is still pending, so a client can resume from the size.
func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET", false)
		return
	}
	requestPath, err := normalizePath(r.URL.Query().Get("path"))
	if err != nil || requestPath == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path query is required", false)
		return
	}
	entry, err := s.core.FSStat(r.Context(), requestPath)
	if err != nil {
		writeCoreError(w, err)
		return
	}
	pending, _ := s.pendingFor(r.Context(), requestPath)
	writeData(w, http.StatusOK, map[string]any{"path": requestPath, "size": entry.Size, "pending": pending})
}

func (s *Server) pendingFor(ctx context.Context, requestPath string) (bool, error) {
	pending, err := s.core.FSPendingUploads(ctx)
	if err != nil {
		return false, err
	}
	for _, upload := range pending {
		if upload.Path == requestPath {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	if !s.csrfAllowed(w, r) {
		return
	}
	var request struct {
		Path string `json:"path"`
	}
	if !decodeWriteBody(w, r, &request) {
		return
	}
	requestPath, err := normalizePath(request.Path)
	if err != nil || requestPath == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path is required", false)
		return
	}
	entry, err := s.core.FSMkdir(s.mutationContext(r), requestPath)
	if err != nil {
		writeCoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, entry)
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	if !s.csrfAllowed(w, r) {
		return
	}
	var request struct {
		Path string `json:"path"`
		Dir  bool   `json:"dir"`
	}
	if !decodeWriteBody(w, r, &request) {
		return
	}
	requestPath, err := normalizePath(request.Path)
	if err != nil || requestPath == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path is required", false)
		return
	}
	if err := s.core.FSRemove(s.mutationContext(r), requestPath, request.Dir); err != nil {
		writeCoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"path": requestPath, "removed": true})
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	if !s.csrfAllowed(w, r) {
		return
	}
	var request struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
	}
	if !decodeWriteBody(w, r, &request) {
		return
	}
	source, err := normalizePath(request.Source)
	if err != nil || source == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "source is required", false)
		return
	}
	destination, err := normalizePath(request.Destination)
	if err != nil || destination == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "destination is required", false)
		return
	}
	if err := s.core.FSRename(s.mutationContext(r), source, destination); err != nil {
		writeCoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"source": source, "destination": destination, "renamed": true})
}

func (s *Server) handleCopy(w http.ResponseWriter, r *http.Request) {
	if !s.csrfAllowed(w, r) {
		return
	}
	var request struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
		Recursive   bool   `json:"recursive"`
		Force       bool   `json:"force"`
	}
	if !decodeWriteBody(w, r, &request) {
		return
	}
	if _, err := normalizePath(request.Source); err != nil || request.Source == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "source is required", false)
		return
	}
	if _, err := normalizePath(request.Destination); err != nil || request.Destination == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "destination is required", false)
		return
	}
	result, err := s.core.FSCopy(s.mutationContext(r), control.FSCopyRequest{
		Source: request.Source, Destination: request.Destination,
		Recursive: request.Recursive, Force: request.Force,
	})
	if err != nil {
		writeCoreError(w, err)
		return
	}
	if len(result) == 0 {
		writeData(w, http.StatusOK, map[string]any{"source": request.Source, "destination": request.Destination})
		return
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) handleTruncate(w http.ResponseWriter, r *http.Request) {
	if !s.csrfAllowed(w, r) {
		return
	}
	var request struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if !decodeWriteBody(w, r, &request) {
		return
	}
	requestPath, err := normalizePath(request.Path)
	if err != nil || requestPath == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path is required", false)
		return
	}
	if request.Size < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "size must be non-negative", false)
		return
	}
	if err := s.core.FSTruncate(s.mutationContext(r), requestPath, request.Size); err != nil {
		writeCoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"path": requestPath, "size": request.Size})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	requestPath, err := s.requestPath(r, "/")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	entries, err := s.core.FSList(r.Context(), requestPath)
	if err != nil {
		writeCoreError(w, err)
		return
	}
	if entries == nil {
		entries = []control.FSEntry{}
	}
	writeData(w, http.StatusOK, map[string]any{"path": requestPath, "entries": entries})
}

func (s *Server) handleStat(w http.ResponseWriter, r *http.Request) {
	requestPath, err := s.requestPath(r, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	if requestPath == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path is required", false)
		return
	}
	entry, err := s.core.FSStat(r.Context(), requestPath)
	if err != nil {
		writeCoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, entry)
}

// requestPath accepts GET query strings and POST JSON bodies so browser clients
// can use the same command-style endpoint.
func (s *Server) requestPath(r *http.Request, fallback string) (string, error) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		value := r.URL.Query().Get("path")
		if value == "" {
			value = fallback
		}
		return normalizePath(value)
	case http.MethodPost:
		defer r.Body.Close()
		body := io.LimitReader(r.Body, 1<<20)
		var request struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(body).Decode(&request); err != nil {
			return "", fmt.Errorf("invalid JSON body: %w", err)
		}
		if request.Path == "" {
			request.Path = fallback
		}
		return normalizePath(request.Path)
	default:
		return "", fmt.Errorf("use GET, HEAD or POST")
	}
}

func normalizePath(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "/") {
		return value, nil
	}
	return "", fmt.Errorf("path must start with /")
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) { s.serveFile(w, r, false) }

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) { s.serveFile(w, r, true) }

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, attachment bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or HEAD", false)
		return
	}
	requestPath, err := s.requestPath(r, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	if requestPath == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path is required", false)
		return
	}
	if !s.acquireStream() {
		writeError(w, http.StatusTooManyRequests, "too_many_streams", "too many concurrent streams", true)
		return
	}
	defer s.releaseStream()

	ctx := r.Context()
	entry, err := s.core.FSStat(ctx, requestPath)
	if err != nil {
		writeCoreError(w, err)
		return
	}
	if entry.IsDir {
		writeError(w, http.StatusBadRequest, "is_a_directory", "path is a directory", false)
		return
	}
	etag := entryETag(entry)
	lastModified := ""
	if !entry.ModTime.IsZero() {
		lastModified = entry.ModTime.UTC().Format(http.TimeFormat)
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")
	if lastModified != "" {
		w.Header().Set("Last-Modified", lastModified)
	}
	if attachment {
		filename := path.Base(requestPath)
		contentType := mime.TypeByExtension(path.Ext(filename))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filename))
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	offset, length := int64(0), int64(0)
	status := http.StatusOK
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" && ifRangeMatches(r.Header.Get("If-Range"), etag, lastModified) {
		start, end, err := parseByteRange(rangeHeader, entry.Size)
		if err != nil {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", entry.Size))
			writeError(w, http.StatusRequestedRangeNotSatisfiable, "range_not_satisfiable", err.Error(), false)
			return
		}
		offset = start
		length = end - start + 1
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, entry.Size))
	}
	if status == http.StatusOK {
		length = entry.Size
	}
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	stream, err := s.core.FSReadStream(ctx, requestPath, offset, length, false)
	if err != nil {
		if status == http.StatusPartialContent {
			// Headers are already computed; fail before Body is written so the
			// client still gets an error status instead of a truncated 206.
			writeCoreError(w, err)
			return
		}
		writeCoreError(w, err)
		return
	}
	defer stream.Close()
	w.WriteHeader(status)
	if _, err := io.Copy(w, stream); err != nil {
		logging.L.Debugf("httpapi: stream %s: %v", requestPath, err)
	}
}

func entryETag(entry control.FSEntry) string {
	return fmt.Sprintf(`"%x-%x-%s"`, entry.Size, entry.ModTime.UnixNano(), strings.ReplaceAll(entry.ID, `"`, ""))
}

func matchesETag(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == etag || candidate == "W/"+etag {
			return true
		}
	}
	return false
}

func ifRangeMatches(header, etag, lastModified string) bool {
	if header == "" {
		return true
	}
	header = strings.TrimSpace(header)
	return header == etag || header == lastModified
}

// parseByteRange parses a single `bytes=` range. Multi-range requests are
// rejected: the API streams one range per request.
func parseByteRange(header string, size int64) (int64, int64, error) {
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		return 0, 0, fmt.Errorf("unsupported range unit")
	}
	spec := strings.TrimSpace(header[len(prefix):])
	if strings.Contains(spec, ",") {
		return 0, 0, fmt.Errorf("multiple ranges are not supported")
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, fmt.Errorf("invalid range")
	}
	startText, endText := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])
	if startText == "" {
		suffix, err := strconv.ParseInt(endText, 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, fmt.Errorf("invalid suffix range")
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, size - 1, nil
	}
	start, err := strconv.ParseInt(startText, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("invalid range start")
	}
	if start >= size {
		return 0, 0, fmt.Errorf("range start beyond end of file")
	}
	end := size - 1
	if endText != "" {
		end, err = strconv.ParseInt(endText, 10, 64)
		if err != nil || end < start {
			return 0, 0, fmt.Errorf("invalid range end")
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end, nil
}

// --- envelopes and error mapping ---------------------------------------

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

type envelope struct {
	Data  any       `json:"data,omitempty"`
	Error *apiError `json:"error,omitempty"`
}

func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Data: data})
}

func writeError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Error: &apiError{Code: code, Message: message, Retryable: retryable}})
}

func writeCoreError(w http.ResponseWriter, err error) {
	status, code, retryable := mapCoreError(err)
	writeError(w, status, code, err.Error(), retryable)
}

func mapCoreError(err error) (int, string, bool) {
	var transport *control.TransportError
	if errors.As(err, &transport) {
		switch transport.Status {
		case http.StatusNotFound:
			return http.StatusNotFound, "not_found", false
		case http.StatusConflict:
			return http.StatusConflict, "conflict", false
		case http.StatusServiceUnavailable:
			return http.StatusServiceUnavailable, "unavailable", true
		case http.StatusGatewayTimeout:
			return http.StatusGatewayTimeout, "timeout", true
		}
	}
	switch {
	case errors.Is(err, drive.ErrNotFound):
		return http.StatusNotFound, "not_found", false
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout", true
	case errors.Is(err, context.Canceled):
		return 499, "canceled", false
	}
	return http.StatusInternalServerError, "internal", false
}
