package control

import (
	"bytes"
	"context"
	"fmt"
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

	"github.com/yinzhenyu/qrypt/internal/contracttest"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func (s *Server) handleRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, runtimeSnapshot())
}

func runtimeSnapshot() RuntimeResponse {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	rss, rssSource, rssOK := contracttest.CurrentProcessRSS()
	return RuntimeResponse{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		GoVersion:     runtime.Version(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		NumCPU:        runtime.NumCPU(),
		NumGoroutine:  runtime.NumGoroutine(),
		Process: ProcessMemory{
			PID:          os.Getpid(),
			RSSBytes:     rss,
			RSSAvailable: rssOK,
			RSSSource:    rssSource,
		},
		Mem: MemStats{
			Alloc:      mem.Alloc,
			TotalAlloc: mem.TotalAlloc,
			Sys:        mem.Sys,
			HeapAlloc:  mem.HeapAlloc,
			HeapSys:    mem.HeapSys,
			NumGC:      mem.NumGC,
		},
	}
}

func (s *Server) handleOps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	provider, ok := s.source.(vfs.DebugActiveProvider)
	if !ok {
		http.Error(w, "active ops not available", http.StatusNotImplemented)
		return
	}
	mounts, err := provider.DebugActiveOps(r.Context(), debugMountQuery(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, ActiveOpsResponse{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Mounts:        mounts,
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
			cacheID := info.CacheID
			if cacheID == "" {
				cacheID = info.RemoteID
			}
			filtered := filterReadCacheFile(mount.Cache.DebugReadCache, cacheID)
			cache := mount.Cache
			cache.DebugReadCache = filtered
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
		mounts = append(mounts, DebugCacheMountStatus{Mount: mount.Identity.Name, Cache: mount.Cache})
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Mount < mounts[j].Mount })
	writeJSON(w, CacheResponse{
		SchemaVersion: snapshot.SchemaVersion,
		GeneratedAt:   snapshot.GeneratedAt,
		Mounts:        mounts,
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
		for _, pending := range mount.PendingUploads() {
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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, HealthResponse{
		API:           APIVersion,
		OK:            true,
		Timestamp:     time.Now(),
		PID:           os.Getpid(),
		DebugStarted:  vfs.DebugStartedAt(),
		GoVersion:     runtime.Version(),
		NumGoroutine:  runtime.NumGoroutine(),
		ListenAddress: s.ListenAddress(),
	})
}

func (s *Server) handleDebugReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	vfs.ResetDebugStartedAt()
	reset := []string{"debug_started_at"}
	if resetter, ok := s.source.(debugResetter); ok {
		if err := resetter.DebugReset(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		reset = append(reset, "vfs_reads")
	}
	writeJSON(w, DebugResetResponse{
		SchemaVersion:  vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:    time.Now(),
		DebugStartedAt: vfs.DebugStartedAt(),
		Reset:          reset,
	})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.debugSnapshot(r))
}

func cloneValues(values url.Values) url.Values {
	out := make(url.Values, len(values))
	for key, items := range values {
		out[key] = append([]string(nil), items...)
	}
	return out
}
