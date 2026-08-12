package control

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

func (s *Server) handleStaging(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	inspector, ok := s.source.(diagnostics.DebugStagingInspector)
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
	injector, ok := s.source.(faultinject.DebugUploadCancelInjector)
	if !ok {
		http.Error(w, "debug upload cancel faults not available", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, DebugUploadCancelFaultsResponse{
			SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
			GeneratedAt:   time.Now(),
			Faults:        injector.DebugUploadCancelFaults(r.Context()),
		})
	case http.MethodPost:
		var req faultinject.DebugUploadCancelRequest
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

func filterDebugStagingMounts(mounts []diagnostics.DebugStagingMount, mountNames []string) []diagnostics.DebugStagingMount {
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

func cacheMountName(snapshot diagnostics.DebugSnapshot, path string) string {
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

func filterReadCacheFile(cache readcache.DebugReadCache, fid string) readcache.DebugReadCache {
	files := cache.Files
	cache.Files = nil
	cache.ChunkCount = 0
	cache.Bytes = 0
	for _, file := range files {
		if file.ID == fid {
			cache.Files = []readcache.DebugReadCacheFile{file}
			cache.ChunkCount = file.ChunkCount
			cache.Bytes = file.Bytes
			return cache
		}
	}
	return cache
}

func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := s.debugSnapshot(r)
	var pending []vfs.PendingUpload
	for _, mount := range snapshot.Mounts {
		for _, item := range mount.PendingUploads() {
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
	uploads := uploadSnapshots(snapshot, includeHistory, filterPath, hasFilter)
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

func (s *Server) handleUploadMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	since, sinceOK, err := parseSinceQuery(r.URL.Query().Get("since"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !sinceOK {
		since = time.Now().Add(-2 * time.Minute)
		sinceOK = true
	}
	limit := parseLimitQuery(r.URL.Query().Get("limit"), 20, 200)
	snapshot := s.debugSnapshot(r)
	includeHistory := parseBoolQuery(r.URL.Query().Get("history"))
	filterPath := cleanVirtual(r.URL.Query().Get("path"))
	hasFilter := r.URL.Query().Get("path") != ""
	uploads := uploadSnapshots(snapshot, includeHistory, filterPath, hasFilter)
	drivers := uploadMemoryDrivers(snapshot, r.URL.Query(), since, sinceOK, limit)
	runtime := runtimeSnapshot()
	writeJSON(w, UploadMemoryResponse{
		SchemaVersion: snapshot.SchemaVersion,
		GeneratedAt:   snapshot.GeneratedAt,
		Runtime:       runtime,
		Uploads:       uploads,
		Drivers:       drivers,
		Diagnostics:   uploadMemoryDiagnostics(runtime, uploads, drivers),
	})
}

func prefixUploadSnapshotPath(upload *upload.UploadSnapshot, mountName string) {
	prefix := "/" + mountName
	upload.Path = joinVirtual(prefix, upload.Path)
	for i := range upload.Events {
		if upload.Events[i].Path != "" {
			upload.Events[i].Path = joinVirtual(prefix, upload.Events[i].Path)
		}
	}
}

func uploadSnapshots(snapshot diagnostics.DebugSnapshot, includeHistory bool, filterPath string, hasFilter bool) []upload.UploadSnapshot {
	var uploads []upload.UploadSnapshot
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
	return uploads
}

func uploadMemoryDrivers(snapshot diagnostics.DebugSnapshot, q url.Values, since time.Time, sinceOK bool, limit int) []UploadMemoryDriver {
	partQuery := cloneValues(q)
	partQuery.Del("operation")
	partQuery.Del("kind")
	drivers := make([]UploadMemoryDriver, 0, len(snapshot.Mounts))
	for _, mount := range snapshot.Mounts {
		if mount.Identity.Driver == nil && len(mount.DriverMetricEvents()) == 0 && len(mount.ActiveUploads()) == 0 {
			continue
		}
		var driverName string
		if mount.Identity.Driver != nil {
			driverName = mount.Identity.Driver.Driver
		}
		if driverName == "" {
			driverName = mount.Identity.DriverName
		}
		events := filterDriverMetricEvents(mount.DriverMetricEvents(), partQuery, since, sinceOK, 0)
		recentParts := make([]drive.MetricEvent, 0, len(events))
		for _, event := range events {
			if !isUploadPartMetric(event) {
				continue
			}
			recentParts = append(recentParts, event)
		}
		recentParts = limitMetricEvents(recentParts, limit)
		drivers = append(drivers, UploadMemoryDriver{
			Mount:         mount.Identity.Name,
			Driver:        driverName,
			ActiveUploads: len(mount.ActiveUploads()),
			RecentParts:   recentParts,
		})
	}
	sort.Slice(drivers, func(i, j int) bool { return drivers[i].Mount < drivers[j].Mount })
	return drivers
}

func isUploadPartMetric(event drive.MetricEvent) bool {
	op := strings.ToLower(event.Operation)
	name := strings.ToLower(event.Name)
	step := strings.ToLower(event.Step)
	return strings.Contains(op, "upload_part") ||
		strings.Contains(op, "part_upload") ||
		strings.Contains(name, "upload_part") ||
		strings.Contains(step, "upload_part")
}

func uploadMemoryDiagnostics(runtime RuntimeResponse, uploads []upload.UploadSnapshot, drivers []UploadMemoryDriver) []UploadMemoryDiagnostic {
	var diagnostics []UploadMemoryDiagnostic
	if len(uploads) > 0 && runtime.Mem.HeapAlloc >= 128*util.MiB {
		diagnostics = append(diagnostics, UploadMemoryDiagnostic{
			Level:   "warn",
			Code:    "heap_high_during_upload",
			Message: "Go heap is high while uploads are active; inspect source buffering and multipart body construction.",
			Extra: map[string]any{
				"heap_alloc":     runtime.Mem.HeapAlloc,
				"active_uploads": len(uploads),
			},
		})
	}
	if runtime.Process.RSSAvailable && runtime.Process.RSSBytes > runtime.Mem.HeapAlloc+128*util.MiB {
		diagnostics = append(diagnostics, UploadMemoryDiagnostic{
			Level:   "info",
			Code:    "rss_exceeds_go_heap",
			Message: "Process RSS is much larger than Go heap; this usually points to non-Go memory, stacks, mmap, or allocator-retained pages.",
			Extra: map[string]any{
				"rss_bytes":  runtime.Process.RSSBytes,
				"heap_alloc": runtime.Mem.HeapAlloc,
			},
		})
	}
	for _, driver := range drivers {
		for _, event := range driver.RecentParts {
			if event.Bytes < 16*util.MiB {
				continue
			}
			diagnostics = append(diagnostics, UploadMemoryDiagnostic{
				Level:   "info",
				Code:    "large_upload_part",
				Mount:   driver.Mount,
				Message: "Recent upload part is large; this is fine when streamed, but it is the first place to inspect if heap grows by part size.",
				Extra: map[string]any{
					"bytes":     event.Bytes,
					"operation": event.Operation,
					"op_id":     event.OpID,
				},
			})
			break
		}
	}
	return diagnostics
}
