package control

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resolver, ok := s.source.(diagnostics.DebugResolver)
	if !ok {
		http.Error(w, "resolve unavailable", http.StatusNotImplemented)
		return
	}

	// Reverse resolve by remote ID.
	if remoteID := r.URL.Query().Get("remote_id"); remoteID != "" {
		if ns, ok := s.source.(diagnostics.RemoteIDResolver); ok {
			info, _, err := ns.DebugResolveByRemoteID(r.Context(), remoteID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			writeJSON(w, ResolveResponse{
				SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
				GeneratedAt:   time.Now(),
				Resolves:      []diagnostics.DebugResolveInfo{*info},
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
	var results []diagnostics.DebugResolveInfo
	for _, p := range paths {
		info, err := resolver.DebugResolve(r.Context(), p, includeRemote)
		if err != nil {
			info = diagnostics.DebugResolveInfo{Path: p, PlainName: "-"}
		}
		results = append(results, info)
	}
	writeJSON(w, ResolveResponse{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Resolves:      results,
	})
}

func (s *Server) handleReadMemory(w http.ResponseWriter, r *http.Request) {
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
	limit := parseLimitQuery(r.URL.Query().Get("limit"), 30, 500)
	snapshot := s.debugSnapshot(r)
	mounts := readMemoryMounts(snapshot, r.URL.Query(), since, sinceOK, limit)
	runtime := runtimeSnapshot()
	writeJSON(w, ReadMemoryResponse{
		SchemaVersion: snapshot.SchemaVersion,
		GeneratedAt:   snapshot.GeneratedAt,
		Runtime:       runtime,
		Mounts:        mounts,
		Diagnostics:   readMemoryDiagnostics(runtime, mounts),
	})
}

func readMemoryMounts(snapshot diagnostics.DebugSnapshot, q url.Values, since time.Time, sinceOK bool, limit int) []ReadMemoryMount {
	filterPath := cleanVirtual(q.Get("path"))
	hasFilter := q.Get("path") != ""
	out := make([]ReadMemoryMount, 0, len(snapshot.Mounts))
	for _, mount := range snapshot.Mounts {
		reads := make([]drive.MetricEvent, 0, len(mount.ReadEvents()))
		phaseCounts := map[string]int{}
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
			if sinceOK && eventTime(event).Before(since) {
				continue
			}
			if !metricEventMatchesQuery(event, q) {
				continue
			}
			phase := firstNonEmptyString(event.Phase, event.Operation, "unknown")
			phaseCounts[phase]++
			reads = append(reads, event)
		}
		sort.Slice(reads, func(i, j int) bool { return eventTime(reads[i]).Before(eventTime(reads[j])) })
		reads = limitMetricEvents(reads, limit)
		if len(phaseCounts) == 0 {
			phaseCounts = nil
		}
		out = append(out, ReadMemoryMount{
			Mount:       mount.Identity.Name,
			Driver:      mount.Identity.DriverName,
			Runtime:     mount.Runtime,
			Cache:       mount.Cache,
			PhaseCounts: phaseCounts,
			RecentReads: reads,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

func readMemoryDiagnostics(runtime RuntimeResponse, mounts []ReadMemoryMount) []ReadMemoryDiagnostic {
	var diagnostics []ReadMemoryDiagnostic
	if runtime.Mem.HeapAlloc >= 128*util.MiB {
		diagnostics = append(diagnostics, ReadMemoryDiagnostic{
			Level:   "warn",
			Code:    "heap_high_during_read",
			Message: "Go heap is high during read activity; inspect hot chunks, async write queue, and large response buffers.",
			Extra: map[string]any{
				"heap_alloc": runtime.Mem.HeapAlloc,
				"heap_sys":   runtime.Mem.HeapSys,
			},
		})
	}
	if runtime.Process.RSSAvailable && runtime.Process.RSSBytes > runtime.Mem.HeapAlloc+128*util.MiB {
		diagnostics = append(diagnostics, ReadMemoryDiagnostic{
			Level:   "info",
			Code:    "rss_exceeds_go_heap",
			Message: "Process RSS is much larger than Go heap; inspect non-Go memory, stacks, mmap, or allocator-retained pages.",
			Extra: map[string]any{
				"rss_bytes":  runtime.Process.RSSBytes,
				"heap_alloc": runtime.Mem.HeapAlloc,
			},
		})
	}
	for _, mount := range mounts {
		if mount.Runtime.HotChunkBytes >= 32*util.MiB {
			diagnostics = append(diagnostics, ReadMemoryDiagnostic{
				Level:   "info",
				Code:    "hot_chunks_high",
				Mount:   mount.Mount,
				Message: "Hot read chunks are a significant resident memory user.",
				Extra: map[string]any{
					"hot_chunk_count": mount.Runtime.HotChunkCount,
					"hot_chunk_bytes": mount.Runtime.HotChunkBytes,
					"hot_chunk_limit": mount.Runtime.HotChunkLimit,
				},
			})
		}
		if mount.Cache.WriteQueueBytes > 0 {
			diagnostics = append(diagnostics, ReadMemoryDiagnostic{
				Level:   "info",
				Code:    "read_cache_write_queue_pending",
				Mount:   mount.Mount,
				Message: "Async read-cache writes are holding copied chunks in memory.",
				Extra: map[string]any{
					"write_queue_len":       mount.Cache.WriteQueueLength,
					"write_queue_bytes":     mount.Cache.WriteQueueBytes,
					"write_queue_max_bytes": mount.Cache.WriteQueueMaxBytes,
				},
			})
		}
	}
	return diagnostics
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
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
			Name:      entry.Name,
			Path:      joinVirtual(parentPath, entry.Name),
			ID:        entry.ID,
			ParentID:  entry.ParentID,
			IsDir:     entry.IsDir,
			Size:      entry.Size,
			ModTime:   entry.ModTime,
			CreatedAt: entry.CreatedAt,
			UpdatedAt: entry.UpdatedAt,
		})
	}
	return out
}

func (s *Server) handleReads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := s.debugSnapshot(r)
	filterPath := cleanVirtual(r.URL.Query().Get("path"))
	hasFilter := r.URL.Query().Get("path") != ""
	since, sinceOK, err := parseSinceQuery(r.URL.Query().Get("since"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit := parseLimitQuery(r.URL.Query().Get("limit"), 200, 2000)
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
			if sinceOK && eventTime(event).Before(since) {
				continue
			}
			if !metricEventMatchesQuery(event, r.URL.Query()) {
				continue
			}
			reads = append(reads, event)
		}
	}
	sort.Slice(reads, func(i, j int) bool { return reads[i].StartedAt.Before(reads[j].StartedAt) })
	reads = limitMetricEvents(reads, limit)
	resp := ReadsResponse{SchemaVersion: snapshot.SchemaVersion, GeneratedAt: snapshot.GeneratedAt, Reads: reads}
	if hasFilter {
		resp.Path = filterPath
	}
	writeJSON(w, resp)
}

func parseSinceQuery(raw string) (time.Time, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false, nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return time.Now().Add(-d), true, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("invalid since %q: use duration like 2m or RFC3339 timestamp", raw)
	}
	return t, true, nil
}

func parseLimitQuery(raw string, def, maxLimit int) int {
	limit := def
	if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && parsed > 0 {
		limit = parsed
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return limit
}

func metricEventMatchesQuery(event drive.MetricEvent, q url.Values) bool {
	if operation := strings.TrimSpace(q.Get("operation")); operation != "" && event.Operation != operation {
		return false
	}
	if phase := strings.TrimSpace(q.Get("phase")); phase != "" && event.Phase != phase {
		return false
	}
	if opID := strings.TrimSpace(q.Get("op_id")); opID != "" && event.OpID != opID && event.ParentOpID != opID {
		return false
	}
	if remoteID := strings.TrimSpace(q.Get("remote_id")); remoteID != "" && event.RemoteID != remoteID {
		return false
	}
	if parseBoolQuery(q.Get("error_only")) && event.Error == "" && event.OK {
		return false
	}
	return true
}

func eventTime(event drive.MetricEvent) time.Time {
	if !event.At.IsZero() {
		return event.At
	}
	if !event.FinishedAt.IsZero() {
		return event.FinishedAt
	}
	return event.StartedAt
}

func limitMetricEvents(events []drive.MetricEvent, limit int) []drive.MetricEvent {
	if limit <= 0 || len(events) <= limit {
		return events
	}
	return append([]drive.MetricEvent(nil), events[len(events)-limit:]...)
}
