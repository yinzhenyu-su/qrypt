package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const APIVersion = "v1"

type Snapshotter interface {
	DebugSnapshot() vfs.DebugSnapshot
}

type Server struct {
	socketPath string
	endpoint   string
	network    string
	address    string
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
	SchemaVersion int                  `json:"schema_version"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Path          string               `json:"path,omitempty"`
	History       bool                 `json:"history"`
	Uploads       []vfs.UploadSnapshot `json:"uploads"`
}

type ReadsResponse struct {
	SchemaVersion int                 `json:"schema_version"`
	GeneratedAt   time.Time           `json:"generated_at"`
	Path          string              `json:"path,omitempty"`
	Reads         []drive.MetricEvent `json:"reads"`
}

type DriversResponse struct {
	SchemaVersion int                  `json:"schema_version"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Drivers       []DebugDriverSummary `json:"drivers"`
}

type MountHealthResponse struct {
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Mounts        []vfs.MountHealth `json:"mounts"`
}

type EventsResponse struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	Events        []logging.Event `json:"events"`
}

type DebugDriverSummary struct {
	Mount        string              `json:"mount"`
	Capabilities []drive.Capability  `json:"capabilities,omitempty"`
	Driver       drive.DebugSnapshot `json:"driver"`
	Metrics      []drive.MetricEvent `json:"metrics,omitempty"`
	Space        *DebugSpaceSummary  `json:"space,omitempty"`
}

type DebugSpaceSummary struct {
	BytesTotal    int64  `json:"bytes_total"`
	BytesFree     int64  `json:"bytes_free"`
	Total         string `json:"total"`
	Free          string `json:"free"`
	Unsupported   bool   `json:"unsupported,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Error         string `json:"error,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
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
	SchemaVersion int                    `json:"schema_version"`
	GeneratedAt   time.Time              `json:"generated_at"`
	Resolve       *vfs.DebugResolveInfo  `json:"resolve,omitempty"`
	Resolves      []vfs.DebugResolveInfo `json:"resolves,omitempty"`
}

type TransferMountContext struct {
	Name         string             `json:"name"`
	Driver       string             `json:"driver,omitempty"`
	Capabilities []drive.Capability `json:"capabilities,omitempty"`
	Encrypted    bool               `json:"encrypted"`
}

type TransferContextResponse struct {
	SchemaVersion     int                  `json:"schema_version"`
	GeneratedAt       time.Time            `json:"generated_at"`
	Source            vfs.DebugResolveInfo `json:"source"`
	Destination       vfs.DebugResolveInfo `json:"destination"`
	DestinationParent vfs.DebugResolveInfo `json:"destination_parent"`
	SourceMount       TransferMountContext `json:"source_mount"`
	DestinationMount  TransferMountContext `json:"destination_mount"`
	Compatible        bool                 `json:"compatible"`
	Warnings          []string             `json:"warnings"`
}

type CacheResponse struct {
	SchemaVersion int                     `json:"schema_version"`
	GeneratedAt   time.Time               `json:"generated_at"`
	Path          string                  `json:"path,omitempty"`
	Resolve       *vfs.DebugResolveInfo   `json:"resolve,omitempty"`
	Mounts        []DebugCacheMountStatus `json:"mounts"`
}

type StagingResponse struct {
	SchemaVersion int                     `json:"schema_version"`
	GeneratedAt   time.Time               `json:"generated_at"`
	Path          string                  `json:"path,omitempty"`
	Mounts        []vfs.DebugStagingMount `json:"mounts"`
}

type DebugCacheMountStatus struct {
	Mount string             `json:"mount"`
	Cache vfs.DebugReadCache `json:"cache"`
}

type ConsistencyResponse struct {
	SchemaVersion int                     `json:"schema_version"`
	GeneratedAt   time.Time               `json:"generated_at"`
	Report        vfs.ConsistencyReport   `json:"report,omitempty"`
	Reports       []vfs.ConsistencyReport `json:"reports,omitempty"`
}

type RuntimeResponse struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	GoVersion     string    `json:"go_version"`
	GOOS          string    `json:"goos"`
	GOARCH        string    `json:"goarch"`
	NumCPU        int       `json:"num_cpu"`
	NumGoroutine  int       `json:"num_goroutine"`
	Mem           MemStats  `json:"mem"`
}

type DebugUploadCancelFaultsResponse struct {
	SchemaVersion int                          `json:"schema_version"`
	GeneratedAt   time.Time                    `json:"generated_at"`
	Faults        []vfs.DebugUploadCancelFault `json:"faults"`
}

type MemStats struct {
	Alloc      uint64 `json:"alloc"`
	TotalAlloc uint64 `json:"total_alloc"`
	Sys        uint64 `json:"sys"`
	HeapAlloc  uint64 `json:"heap_alloc"`
	HeapSys    uint64 `json:"heap_sys"`
	NumGC      uint32 `json:"num_gc"`
}

func NewServer(socketPath string, source Snapshotter) (*Server, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("control: socket path required")
	}
	if source == nil {
		return nil, fmt.Errorf("control: snapshot source required")
	}
	network, address, err := listenEndpoint(socketPath)
	if err != nil {
		return nil, err
	}
	server := &Server{
		endpoint: socketPath,
		network:  network,
		address:  address,
		source:   source,
	}
	if network == "unix" {
		server.socketPath = address
	}
	return server, nil
}

func (s *Server) Start(ctx context.Context) error {
	if s.network == "unix" {
		if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
			return err
		}
		if err := removeStaleSocket(s.socketPath); err != nil {
			return err
		}
	}
	listener, err := net.Listen(s.network, s.address)
	if err != nil {
		return err
	}
	if s.network == "unix" {
		if err := os.Chmod(s.socketPath, 0o600); err != nil {
			listener.Close()
			return err
		}
	}
	s.listener = listener
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/state", s.handleState)
	mux.HandleFunc("/v1/pending", s.handlePending)
	mux.HandleFunc("/v1/uploads", s.handleUploads)
	mux.HandleFunc("/v1/reads", s.handleReads)
	mux.HandleFunc("/v1/bench", s.handleBench)
	mux.HandleFunc("/v1/driver", s.handleDriver)
	mux.HandleFunc("/v1/driver/bench", s.handleBench)
	mux.HandleFunc("/v1/driver/test", s.handleDriverTest)
	mux.HandleFunc("/v1/probe/driver", s.handleDriverTest)
	mux.HandleFunc("/v1/mounts/health", s.handleMountHealth)
	mux.HandleFunc("/v1/events", s.handleEvents)
	mux.HandleFunc("/v1/list", s.handleList)
	mux.HandleFunc("/v1/resolve", s.handleResolve)
	mux.HandleFunc("/v1/transfer/context", s.handleTransferContext)
	mux.HandleFunc("/v1/cache", s.handleCache)
	mux.HandleFunc("/v1/staging", s.handleStaging)
	mux.HandleFunc("/v1/debug/faults/upload-cancel", s.handleDebugUploadCancelFaults)
	mux.HandleFunc("/v1/consistency", s.handleConsistency)
	mux.HandleFunc("/v1/runtime", s.handleRuntime)
	mux.HandleFunc("/v1/goroutines", s.handleGoroutines)
	s.server = &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = s.Close(context.Background())
	}()
	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logging.L.Warnf("[CONTROL] server stopped with error listen=%q err=%v", s.ListenAddress(), err)
		}
	}()
	logging.L.Infof("[CONTROL] listening %s", s.ListenAddress())
	return nil
}

func (s *Server) handleRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	writeJSON(w, RuntimeResponse{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		GoVersion:     runtime.Version(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		NumCPU:        runtime.NumCPU(),
		NumGoroutine:  runtime.NumGoroutine(),
		Mem: MemStats{
			Alloc:      mem.Alloc,
			TotalAlloc: mem.TotalAlloc,
			Sys:        mem.Sys,
			HeapAlloc:  mem.HeapAlloc,
			HeapSys:    mem.HeapSys,
			NumGC:      mem.NumGC,
		},
	})
}

func (s *Server) handleGoroutines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	debug := 1
	if raw := r.URL.Query().Get("debug"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			debug = parsed
		}
	}
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, debug); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
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

	// Reverse resolve by remote ID.
	if remoteID := r.URL.Query().Get("remote_id"); remoteID != "" {
		if ns, ok := s.source.(interface {
			DebugResolveByRemoteID(ctx context.Context, remoteID string) (*vfs.DebugResolveInfo, string, error)
		}); ok {
			info, _, err := ns.DebugResolveByRemoteID(r.Context(), remoteID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			writeJSON(w, ResolveResponse{
				SchemaVersion: vfs.DebugSnapshotSchemaVersion,
				GeneratedAt:   time.Now(),
				Resolves:      []vfs.DebugResolveInfo{*info},
			})
			return
		}
		http.Error(w, "reverse resolve not available", http.StatusNotImplemented)
		return
	}

	includeRemote := parseBoolQuery(r.URL.Query().Get("include_remote_name"))
	paths := r.URL.Query()["path"]
	if len(paths) == 0 {
		paths = []string{"/"}
	}
	var results []vfs.DebugResolveInfo
	for _, p := range paths {
		info, err := resolver.DebugResolve(r.Context(), p, includeRemote)
		if err != nil {
			info = vfs.DebugResolveInfo{Path: p, PlainName: "-"}
		}
		results = append(results, info)
	}
	writeJSON(w, ResolveResponse{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Resolves:      results,
	})
}

func (s *Server) handleTransferContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sourcePath := r.URL.Query().Get("source")
	destPath := r.URL.Query().Get("dest")
	if sourcePath == "" || destPath == "" {
		http.Error(w, "source and dest are required", http.StatusBadRequest)
		return
	}
	resolver, ok := s.source.(vfs.DebugResolver)
	if !ok {
		http.Error(w, "resolve unavailable", http.StatusNotImplemented)
		return
	}
	source, err := resolver.DebugResolve(r.Context(), sourcePath, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	destination, err := resolver.DebugResolve(r.Context(), destPath, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	destinationParent, err := resolver.DebugResolve(r.Context(), filepath.Dir(cleanVirtual(destPath)), true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	snapshot := s.source.DebugSnapshot()
	resp := TransferContextResponse{
		SchemaVersion: snapshot.SchemaVersion, GeneratedAt: snapshot.GeneratedAt,
		Source: source, Destination: destination, DestinationParent: destinationParent,
		Warnings: []string{},
	}
	resp.SourceMount = transferMountContext(snapshot, source.Mount)
	resp.DestinationMount = transferMountContext(snapshot, destinationParent.Mount)
	if source.RemoteID == "" {
		resp.Warnings = append(resp.Warnings, "source does not resolve to a remote entry")
	}
	if source.IsDir {
		resp.Warnings = append(resp.Warnings, "source is a directory; recursive traversal is required")
	}
	if destinationParent.RemoteID == "" || !destinationParent.IsDir {
		resp.Warnings = append(resp.Warnings, "destination parent does not resolve to a remote directory")
	}
	if !hasCapability(resp.DestinationMount.Capabilities, drive.CapabilitySourceUploader) {
		resp.Warnings = append(resp.Warnings, "destination driver does not support source upload")
	}
	resp.Compatible = source.RemoteID != "" &&
		destinationParent.RemoteID != "" &&
		destinationParent.IsDir &&
		hasCapability(resp.DestinationMount.Capabilities, drive.CapabilitySourceUploader)
	writeJSON(w, resp)
}

func transferMountContext(snapshot vfs.DebugSnapshot, mountName string) TransferMountContext {
	for _, mount := range snapshot.Mounts {
		if mount.Identity.Name == mountName || (mountName == "" && len(snapshot.Mounts) == 1) {
			return TransferMountContext{
				Name:         mount.Identity.Name,
				Driver:       mount.Identity.DriverName,
				Capabilities: mount.Identity.Capabilities,
				Encrypted:    mount.Identity.Encrypted,
			}
		}
	}
	return TransferMountContext{Name: mountName}
}

func hasCapability(capabilities []drive.Capability, target drive.Capability) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

func (s *Server) handleCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := s.debugSnapshot(r)
	path := r.URL.Query().Get("path")
	if path != "" {
		resolver, ok := s.source.(vfs.DebugResolver)
		if !ok {
			http.Error(w, "resolve unavailable", http.StatusNotImplemented)
			return
		}
		info, err := resolver.DebugResolve(r.Context(), path, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		mountName := cacheMountName(snapshot, info.Path)
		for _, mount := range snapshot.Mounts {
			if mount.Identity.Name != mountName {
				continue
			}
			cache := filterReadCacheFile(mount.ReadCacheState(), info.RemoteID)
			writeJSON(w, CacheResponse{
				SchemaVersion: snapshot.SchemaVersion,
				GeneratedAt:   snapshot.GeneratedAt,
				Path:          info.Path,
				Resolve:       &info,
				Mounts:        []DebugCacheMountStatus{{Mount: mount.Identity.Name, Cache: cache}},
			})
			return
		}
		http.Error(w, "cache mount not found", http.StatusNotFound)
		return
	}
	var mounts []DebugCacheMountStatus
	for _, mount := range snapshot.Mounts {
		mounts = append(mounts, DebugCacheMountStatus{Mount: mount.Identity.Name, Cache: mount.ReadCacheState()})
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Mount < mounts[j].Mount })
	writeJSON(w, CacheResponse{
		SchemaVersion: snapshot.SchemaVersion,
		GeneratedAt:   snapshot.GeneratedAt,
		Mounts:        mounts,
	})
}

func (s *Server) handleStaging(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	inspector, ok := s.source.(vfs.DebugStagingInspector)
	if !ok {
		http.Error(w, "staging unavailable", http.StatusNotImplemented)
		return
	}
	report, err := inspector.DebugStaging(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	snapshot := s.debugSnapshot(r)
	if mounts := debugMountQuery(r); len(mounts) > 0 {
		report.Mounts = filterDebugStagingMounts(report.Mounts, mounts)
	}
	writeJSON(w, StagingResponse{
		SchemaVersion: snapshot.SchemaVersion,
		GeneratedAt:   snapshot.GeneratedAt,
		Path:          report.Path,
		Mounts:        report.Mounts,
	})
}

func (s *Server) handleDebugUploadCancelFaults(w http.ResponseWriter, r *http.Request) {
	injector, ok := s.source.(vfs.DebugUploadCancelInjector)
	if !ok {
		http.Error(w, "debug upload cancel faults not available", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, DebugUploadCancelFaultsResponse{
			SchemaVersion: vfs.DebugSnapshotSchemaVersion,
			GeneratedAt:   time.Now(),
			Faults:        injector.DebugUploadCancelFaults(r.Context()),
		})
	case http.MethodPost:
		var req vfs.DebugUploadCancelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, err := injector.DebugInjectUploadCancel(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, result)
	case http.MethodDelete:
		if err := injector.DebugClearUploadCancel(r.Context(), r.URL.Query().Get("id")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func filterDebugStagingMounts(mounts []vfs.DebugStagingMount, mountNames []string) []vfs.DebugStagingMount {
	if len(mountNames) == 0 {
		return mounts
	}
	allowed := debugMountSet(mountNames)
	filtered := mounts[:0]
	for _, mount := range mounts {
		if allowed[mount.Mount] {
			filtered = append(filtered, mount)
		}
	}
	return filtered
}

func cacheMountName(snapshot vfs.DebugSnapshot, path string) string {
	if snapshot.Kind != "namespace" {
		if len(snapshot.Mounts) == 1 {
			return snapshot.Mounts[0].Identity.Name
		}
		return ""
	}
	path = strings.Trim(strings.TrimPrefix(path, "/"), "/")
	if idx := strings.Index(path, "/"); idx >= 0 {
		return path[:idx]
	}
	return path
}

func filterReadCacheFile(cache vfs.DebugReadCache, fid string) vfs.DebugReadCache {
	files := cache.Files
	cache.Files = nil
	cache.ChunkCount = 0
	cache.Bytes = 0
	for _, file := range files {
		if file.ID == fid {
			cache.Files = []vfs.DebugReadCacheFile{file}
			cache.ChunkCount = file.ChunkCount
			cache.Bytes = file.Bytes
			return cache
		}
	}
	return cache
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
	dir := r.URL.Query().Get("dir")
	if path == "" && dir == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	if dir != "" {
		reports, err := s.consistencyReports(r.Context(), checker, dir, parseBoolQuery(r.URL.Query().Get("recursive")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, ConsistencyResponse{
			SchemaVersion: vfs.DebugSnapshotSchemaVersion,
			GeneratedAt:   time.Now(),
			Reports:       reports,
		})
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

func (s *Server) consistencyReports(ctx context.Context, checker vfs.DebugConsistencyChecker, dir string, recursive bool) ([]vfs.ConsistencyReport, error) {
	lister, ok := s.source.(vfs.RemoteLister)
	if !ok {
		return nil, fmt.Errorf("remote list unavailable")
	}
	dir = cleanVirtual(dir)
	entries, err := lister.RemoteList(ctx, dir)
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for _, entry := range entries {
		path := joinVirtual(dir, entry.Name)
		if entry.IsDir && recursive {
			nested, err := s.consistencyReports(ctx, checker, path, recursive)
			if err != nil {
				return nil, err
			}
			for _, report := range nested {
				paths[report.Path] = true
			}
			continue
		}
		paths[path] = true
	}
	for _, mount := range s.source.DebugSnapshot().Mounts {
		for _, pending := range mount.PendingFiles() {
			path := pending.Path
			if mount.Identity.Name != "" {
				path = joinVirtual("/"+mount.Identity.Name, path)
			}
			path = cleanVirtual(path)
			if path == dir || strings.HasPrefix(path, strings.TrimRight(dir, "/")+"/") {
				if !recursive && filepath.Dir(path) != dir {
					continue
				}
				paths[path] = true
			}
		}
	}
	var all []string
	for path := range paths {
		all = append(all, path)
	}
	sort.Strings(all)
	reports := make([]vfs.ConsistencyReport, 0, len(all))
	for _, path := range all {
		report, err := checker.DebugConsistency(ctx, path)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
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
	events := logging.L.Events(level, limit)
	path := r.URL.Query().Get("path")
	component := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("component")))
	if path != "" || component != "" {
		filtered := events[:0]
		for _, event := range events {
			if path != "" && !strings.Contains(event.Message, path) {
				continue
			}
			if component != "" && eventComponent(event.Message) != component {
				continue
			}
			filtered = append(filtered, event)
		}
		events = filtered
	}
	writeJSON(w, EventsResponse{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Events:        events,
	})
}

func eventComponent(message string) string {
	if !strings.HasPrefix(message, "[") {
		return ""
	}
	end := strings.Index(message, "]")
	if end <= 1 {
		return ""
	}
	return strings.ToUpper(message[1:end])
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

func listenEndpoint(endpoint string) (network, address string, err error) {
	endpoint = strings.TrimSpace(endpoint)
	switch {
	case endpoint == "":
		return "", "", fmt.Errorf("control: listen endpoint required")
	case strings.HasPrefix(endpoint, "unix:"):
		path := strings.TrimPrefix(endpoint, "unix:")
		if path == "" {
			return "", "", fmt.Errorf("control: unix listen path required")
		}
		return "unix", osutil.ExpandHome(path), nil
	case strings.HasPrefix(endpoint, "tcp:"):
		return tcpListenEndpoint(strings.TrimPrefix(endpoint, "tcp:"))
	case strings.HasPrefix(endpoint, "http://"):
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return "", "", err
		}
		return tcpListenEndpoint(parsed.Host)
	case strings.HasPrefix(endpoint, "https://"):
		return "", "", fmt.Errorf("control: https listen is not supported")
	}
	if _, _, err := net.SplitHostPort(endpoint); err == nil {
		return tcpListenEndpoint(endpoint)
	}
	return "unix", osutil.ExpandHome(endpoint), nil
}

func tcpListenEndpoint(address string) (string, string, error) {
	if address == "" {
		return "", "", fmt.Errorf("control: tcp listen address required")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return "", "", err
	}
	if host == "" {
		return "", "", fmt.Errorf("control: tcp debug listen must bind to loopback, got %q", address)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", "", fmt.Errorf("control: tcp debug listen must bind to loopback, got %q", address)
	}
	return "tcp", address, nil
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

func (s *Server) ListenAddress() string {
	if s.listener != nil {
		return s.network + ":" + s.listener.Addr().String()
	}
	if s.network == "unix" {
		return "unix:" + s.socketPath
	}
	return s.network + ":" + s.address
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
	writeJSON(w, s.debugSnapshot(r))
}

func (s *Server) debugSnapshot(r *http.Request) vfs.DebugSnapshot {
	mounts := debugMountQuery(r)
	if len(mounts) == 0 {
		return s.source.DebugSnapshot()
	}
	if filtered, ok := s.source.(vfs.DebugMountSnapshotter); ok {
		return filtered.DebugSnapshotForMounts(mounts)
	}
	return filterDebugSnapshotMounts(s.source.DebugSnapshot(), mounts)
}

func debugMountQuery(r *http.Request) []string {
	var out []string
	for _, name := range r.URL.Query()["mount"] {
		name = strings.TrimSpace(name)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func filterDebugSnapshotMounts(snapshot vfs.DebugSnapshot, mountNames []string) vfs.DebugSnapshot {
	if len(mountNames) == 0 {
		return snapshot
	}
	allowed := debugMountSet(mountNames)
	filtered := snapshot.Mounts[:0]
	for _, mount := range snapshot.Mounts {
		if allowed[mount.Identity.Name] {
			filtered = append(filtered, mount)
		}
	}
	snapshot.Mounts = filtered
	return snapshot
}

func debugMountSet(mountNames []string) map[string]bool {
	set := map[string]bool{}
	for _, name := range mountNames {
		name = strings.Trim(strings.TrimSpace(name), "/")
		if name != "" {
			set[name] = true
		}
	}
	return set
}

func debugMountAllowed(mountName string, mountNames []string) bool {
	if len(mountNames) == 0 {
		return true
	}
	return debugMountSet(mountNames)[mountName]
}

func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := s.debugSnapshot(r)
	var pending []vfs.PendingFile
	for _, mount := range snapshot.Mounts {
		for _, item := range mount.PendingFiles() {
			if snapshot.Kind == "namespace" && mount.Identity.Name != "" {
				item.Path = joinVirtual("/"+mount.Identity.Name, item.Path)
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
	snapshot := s.debugSnapshot(r)
	filterPath := cleanVirtual(r.URL.Query().Get("path"))
	hasFilter := r.URL.Query().Get("path") != ""
	includeHistory := parseBoolQuery(r.URL.Query().Get("history"))
	var uploads []vfs.UploadSnapshot
	for _, mount := range snapshot.Mounts {
		for _, item := range mount.ActiveUploads() {
			if snapshot.Kind == "namespace" && mount.Identity.Name != "" {
				prefixUploadSnapshotPath(&item, mount.Identity.Name)
			}
			if hasFilter && cleanVirtual(item.Path) != filterPath {
				continue
			}
			uploads = append(uploads, item)
		}
		if includeHistory {
			for _, item := range mount.HistoricalUploads() {
				if snapshot.Kind == "namespace" && mount.Identity.Name != "" {
					prefixUploadSnapshotPath(&item, mount.Identity.Name)
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

func prefixUploadSnapshotPath(upload *vfs.UploadSnapshot, mountName string) {
	prefix := "/" + mountName
	upload.Path = joinVirtual(prefix, upload.Path)
	for i := range upload.Events {
		if upload.Events[i].Path != "" {
			upload.Events[i].Path = joinVirtual(prefix, upload.Events[i].Path)
		}
	}
}

func (s *Server) handleReads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := s.debugSnapshot(r)
	filterPath := cleanVirtual(r.URL.Query().Get("path"))
	hasFilter := r.URL.Query().Get("path") != ""
	var reads []drive.MetricEvent
	for _, mount := range snapshot.Mounts {
		for _, event := range mount.ReadEvents() {
			if event.Mount == "" {
				event.Mount = mount.Identity.Name
			}
			if event.Driver == "" {
				event.Driver = mount.Identity.DriverName
			}
			if snapshot.Kind == "namespace" && mount.Identity.Name != "" {
				event.Path = joinVirtual("/"+mount.Identity.Name, event.Path)
			}
			if hasFilter && cleanVirtual(event.Path) != filterPath {
				continue
			}
			reads = append(reads, event)
		}
	}
	sort.Slice(reads, func(i, j int) bool { return reads[i].StartedAt.Before(reads[j].StartedAt) })
	resp := ReadsResponse{SchemaVersion: snapshot.SchemaVersion, GeneratedAt: snapshot.GeneratedAt, Reads: reads}
	if hasFilter {
		resp.Path = filterPath
	}
	writeJSON(w, resp)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
	return vfs.CleanVirtualPath(path)
}

func parseBoolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
