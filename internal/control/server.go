package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const APIVersion = "v1"

type Snapshotter interface {
	DebugSnapshot() vfs.DebugSnapshot
}

type Server struct {
	socketPath string
	source     Snapshotter
	server     *http.Server
	listener   net.Listener
}

type HealthResponse struct {
	API       string    `json:"api"`
	OK        bool      `json:"ok"`
	Timestamp time.Time `json:"timestamp"`
}

type PendingResponse struct {
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Pending       []vfs.PendingFile `json:"pending"`
}

type UploadsResponse struct {
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Path          string            `json:"path,omitempty"`
	History       bool              `json:"history"`
	Uploads       []vfs.DebugUpload `json:"uploads"`
}

type DriversResponse struct {
	SchemaVersion int                  `json:"schema_version"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Drivers       []DebugDriverSummary `json:"drivers"`
}

type EventsResponse struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	Events        []logging.Event `json:"events"`
}

type DebugDriverSummary struct {
	Mount  string              `json:"mount"`
	Driver drive.DebugSnapshot `json:"driver"`
}

type ListResponse struct {
	SchemaVersion int         `json:"schema_version"`
	GeneratedAt   time.Time   `json:"generated_at"`
	Path          string      `json:"path"`
	Source        string      `json:"source"`
	Entries       []ListEntry `json:"entries"`
}

type ListEntry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	ID       string    `json:"id"`
	ParentID string    `json:"parent_id"`
	IsDir    bool      `json:"is_dir"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"mod_time,omitempty"`
}

type ResolveResponse struct {
	SchemaVersion int                  `json:"schema_version"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Resolve       vfs.DebugResolveInfo `json:"resolve"`
}

type CacheResponse struct {
	SchemaVersion int                     `json:"schema_version"`
	GeneratedAt   time.Time               `json:"generated_at"`
	Mounts        []DebugCacheMountStatus `json:"mounts"`
}

type DebugCacheMountStatus struct {
	Mount string             `json:"mount"`
	Cache vfs.DebugReadCache `json:"cache"`
}

type TasksResponse struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	Tasks         []vfs.DebugTask `json:"tasks"`
}

type ConsistencyResponse struct {
	SchemaVersion int                   `json:"schema_version"`
	GeneratedAt   time.Time             `json:"generated_at"`
	Report        vfs.ConsistencyReport `json:"report"`
}

func NewServer(socketPath string, source Snapshotter) (*Server, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("control: socket path required")
	}
	if source == nil {
		return nil, fmt.Errorf("control: snapshot source required")
	}
	return &Server{socketPath: expandHome(socketPath), source: source}, nil
}

func (s *Server) Start(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
		return err
	}
	if err := removeStaleSocket(s.socketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		listener.Close()
		return err
	}
	s.listener = listener
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/state", s.handleState)
	mux.HandleFunc("/v1/pending", s.handlePending)
	mux.HandleFunc("/v1/uploads", s.handleUploads)
	mux.HandleFunc("/v1/driver", s.handleDriver)
	mux.HandleFunc("/v1/events", s.handleEvents)
	mux.HandleFunc("/v1/list", s.handleList)
	mux.HandleFunc("/v1/resolve", s.handleResolve)
	mux.HandleFunc("/v1/cache", s.handleCache)
	mux.HandleFunc("/v1/tasks", s.handleTasks)
	mux.HandleFunc("/v1/consistency", s.handleConsistency)
	s.server = &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = s.Close(context.Background())
	}()
	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logging.L.Warnf("[CONTROL] server stopped with error socket=%q err=%v", s.socketPath, err)
		}
	}()
	logging.L.Infof("[CONTROL] listening socket=%q", s.socketPath)
	return nil
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resolver, ok := s.source.(vfs.DebugResolver)
	if !ok {
		http.Error(w, "resolve unavailable", http.StatusNotImplemented)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	info, err := resolver.DebugResolve(r.Context(), path, parseBoolQuery(r.URL.Query().Get("include_remote_name")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, ResolveResponse{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Resolve:       info,
	})
}

func (s *Server) handleCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := s.source.DebugSnapshot()
	var mounts []DebugCacheMountStatus
	for _, mount := range snapshot.Mounts {
		mounts = append(mounts, DebugCacheMountStatus{Mount: mount.Name, Cache: mount.ReadCache})
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Mount < mounts[j].Mount })
	writeJSON(w, CacheResponse{
		SchemaVersion: snapshot.SchemaVersion,
		GeneratedAt:   snapshot.GeneratedAt,
		Mounts:        mounts,
	})
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lister, ok := s.source.(vfs.DebugTaskLister)
	if !ok {
		http.Error(w, "tasks unavailable", http.StatusNotImplemented)
		return
	}
	tasks := lister.DebugTasks()
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Type == tasks[j].Type {
			return tasks[i].Path < tasks[j].Path
		}
		return tasks[i].Type < tasks[j].Type
	})
	writeJSON(w, TasksResponse{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Tasks:         tasks,
	})
}

func (s *Server) handleConsistency(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	checker, ok := s.source.(vfs.DebugConsistencyChecker)
	if !ok {
		http.Error(w, "consistency unavailable", http.StatusNotImplemented)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	report, err := checker.DebugConsistency(r.Context(), path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, ConsistencyResponse{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Report:        report,
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	level := logging.ParseLevel(r.URL.Query().Get("level"))
	if level < logging.LevelWarn {
		level = logging.LevelWarn
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	writeJSON(w, EventsResponse{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Events:        logging.L.Events(level, limit),
	})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lister, ok := s.source.(vfs.RemoteLister)
	if !ok {
		http.Error(w, "remote list unavailable", http.StatusNotImplemented)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	entries, err := lister.RemoteList(r.Context(), path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, ListResponse{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Path:          cleanVirtual(path),
		Source:        "remote",
		Entries:       listEntries(cleanVirtual(path), entries),
	})
}

func listEntries(parentPath string, entries []drive.Entry) []ListEntry {
	out := make([]ListEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ListEntry{
			Name:     entry.Name,
			Path:     joinVirtual(parentPath, entry.Name),
			ID:       entry.ID,
			ParentID: entry.ParentID,
			IsDir:    entry.IsDir,
			Size:     entry.Size,
			ModTime:  entry.ModTime,
		})
	}
	return out
}

func removeStaleSocket(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("control: socket already in use: %s", path)
	}
	return os.Remove(path)
}

func (s *Server) Close(ctx context.Context) error {
	if s.server != nil {
		_ = s.server.Shutdown(ctx)
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
	}
	return nil
}

func (s *Server) SocketPath() string {
	return s.socketPath
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, HealthResponse{API: APIVersion, OK: true, Timestamp: time.Now()})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.source.DebugSnapshot())
}

func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := s.source.DebugSnapshot()
	var pending []vfs.PendingFile
	for _, mount := range snapshot.Mounts {
		for _, item := range mount.Pending {
			if snapshot.Kind == "namespace" && mount.Name != "" {
				item.Path = joinVirtual("/"+mount.Name, item.Path)
			}
			pending = append(pending, item)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].Path < pending[j].Path
	})
	writeJSON(w, PendingResponse{
		SchemaVersion: snapshot.SchemaVersion,
		GeneratedAt:   snapshot.GeneratedAt,
		Pending:       pending,
	})
}

func (s *Server) handleUploads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := s.source.DebugSnapshot()
	filterPath := cleanVirtual(r.URL.Query().Get("path"))
	hasFilter := r.URL.Query().Get("path") != ""
	includeHistory := parseBoolQuery(r.URL.Query().Get("history"))
	var uploads []vfs.DebugUpload
	for _, mount := range snapshot.Mounts {
		for _, item := range mount.Uploads {
			if snapshot.Kind == "namespace" && mount.Name != "" {
				item.Path = joinVirtual("/"+mount.Name, item.Path)
			}
			if hasFilter && cleanVirtual(item.Path) != filterPath {
				continue
			}
			uploads = append(uploads, item)
		}
		if includeHistory {
			for _, item := range mount.UploadHistory {
				if snapshot.Kind == "namespace" && mount.Name != "" {
					item.Path = joinVirtual("/"+mount.Name, item.Path)
				}
				if hasFilter && cleanVirtual(item.Path) != filterPath {
					continue
				}
				uploads = append(uploads, item)
			}
		}
	}
	sort.Slice(uploads, func(i, j int) bool {
		if uploads[i].UpdatedAt.Equal(uploads[j].UpdatedAt) {
			return uploads[i].Path < uploads[j].Path
		}
		return uploads[i].UpdatedAt.Before(uploads[j].UpdatedAt)
	})
	resp := UploadsResponse{
		SchemaVersion: snapshot.SchemaVersion,
		GeneratedAt:   snapshot.GeneratedAt,
		History:       includeHistory,
		Uploads:       uploads,
	}
	if hasFilter {
		resp.Path = filterPath
	}
	writeJSON(w, resp)
}

func (s *Server) handleDriver(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := s.source.DebugSnapshot()
	var drivers []DebugDriverSummary
	for _, mount := range snapshot.Mounts {
		if mount.Driver == nil {
			continue
		}
		drivers = append(drivers, DebugDriverSummary{Mount: mount.Name, Driver: *mount.Driver})
	}
	sort.Slice(drivers, func(i, j int) bool {
		return drivers[i].Mount < drivers[j].Mount
	})
	writeJSON(w, DriversResponse{
		SchemaVersion: snapshot.SchemaVersion,
		GeneratedAt:   snapshot.GeneratedAt,
		Drivers:       drivers,
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if len(path) >= 2 && path[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func joinVirtual(parent, child string) string {
	if child == "" || child == "/" {
		return parent
	}
	if child[0] == '/' {
		child = child[1:]
	}
	if parent == "/" {
		return "/" + child
	}
	return parent + "/" + child
}

func cleanVirtual(path string) string {
	if path == "" {
		return "/"
	}
	cleaned := filepath.Clean("/" + strings.TrimPrefix(path, "/"))
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func parseBoolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
