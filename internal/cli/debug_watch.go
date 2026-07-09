package cli

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func watchDebugAI(ctx context.Context, path string, duration, interval time.Duration, eventLimit int) debugAIWatchReport {
	startedAt := time.Now()
	report := debugAIWatchReport{
		SchemaVersion: debugAIReportSchemaVersion,
		GeneratedAt:   startedAt,
		Command:       "watch",
		Socket:        debugSocketFromContext(ctx),
		Path:          path,
		StartedAt:     startedAt,
		Duration:      duration.String(),
		Interval:      interval.String(),
		Diagnostics:   []debugAIDiagnostic{},
	}
	deadline := startedAt.Add(duration)
	for {
		sample := sampleDebugAIWatch(ctx, path, eventLimit)
		report.Samples = append(report.Samples, sample)
		report.Errors = append(report.Errors, sample.Errors...)
		if time.Now().Add(interval).After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			report.Errors = append(report.Errors, debugAIError{Endpoint: "watch", Message: ctx.Err().Error()})
			report.EndedAt = time.Now()
			addWatchDiagnostics(&report.Diagnostics, report)
			return report
		case <-time.After(interval):
		}
	}
	report.EndedAt = time.Now()
	addWatchDiagnostics(&report.Diagnostics, report)
	return report
}

func sampleDebugAIWatch(ctx context.Context, path string, eventLimit int) debugAIWatchSample {
	sample := debugAIWatchSample{At: time.Now(), Path: path}
	var health *control.HealthResponse
	debugGetJSON(ctx, "/v1/health", &health, &sample.Errors)
	if health != nil {
		ok := health.OK
		sample.HealthOK = &ok
	}
	var state *vfs.DebugSnapshot
	debugGetJSON(ctx, "/v1/state", &state, &sample.Errors)
	var uploads *control.UploadsResponse
	debugGetJSON(ctx, "/v1/uploads?history=1", &uploads, &sample.Errors)
	if uploads != nil {
		sample.Uploads = uploads.Uploads
	}
	var events *control.EventsResponse
	debugGetJSON(ctx, "/v1/events?level=warn&limit="+url.QueryEscape(fmt.Sprintf("%d", eventLimit)), &events, &sample.Errors)
	if events != nil {
		for _, event := range events.Events {
			sample.Events = append(sample.Events, controlEventSummary{
				ID:         event.ID,
				Time:       event.Time,
				Level:      event.Level,
				Component:  debugEventComponent(event.Message),
				Message:    event.Message,
				Suppressed: event.Suppressed,
			})
		}
	}
	var staging *control.StagingResponse
	debugGetJSON(ctx, "/v1/staging", &staging, &sample.Errors)
	var cache *control.CacheResponse
	debugGetJSON(ctx, "/v1/cache", &cache, &sample.Errors)
	sample.Mounts = watchMountSummaries(state, staging, cache)
	if path != "" {
		addWatchPathSample(ctx, &sample, path, eventLimit)
	}
	return sample
}

func watchMountSummaries(state *vfs.DebugSnapshot, staging *control.StagingResponse, cache *control.CacheResponse) []debugAIWatchMount {
	if state == nil {
		return nil
	}
	stagingByMount := map[string]vfs.DebugStagingMount{}
	if staging != nil {
		for _, item := range staging.Mounts {
			stagingByMount[item.Mount] = item
		}
	}
	cacheByMount := map[string]vfs.DebugReadCache{}
	if cache != nil {
		for _, item := range cache.Mounts {
			cacheByMount[item.Mount] = item.Cache
		}
	}
	out := make([]debugAIWatchMount, 0, len(state.Mounts))
	for _, mount := range state.Mounts {
		activeUploads := 0
		for _, upload := range mount.Uploads {
			if upload.State == "uploading" {
				activeUploads++
			}
		}
		item := debugAIWatchMount{
			Name:           mount.Name,
			Driver:         mount.DriverName,
			Encrypted:      mount.Encrypted,
			PendingUploads: len(mount.Pending),
			ActiveUploads:  activeUploads,
		}
		if staging, ok := stagingByMount[mount.Name]; ok {
			item.StagingFiles = staging.StagingCount
			item.StagingOrphans = staging.OrphanCount
		}
		if cache, ok := cacheByMount[mount.Name]; ok {
			item.ReadCacheFiles = cache.FileCount
			item.ReadCacheBytes = cache.Bytes
			item.ReadCacheHits = cache.Hits
			item.ReadCacheMisses = cache.Misses
			item.LastCacheGetErr = cache.LastGetError
			item.LastCachePutErr = cache.LastPutError
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func addWatchPathSample(ctx context.Context, sample *debugAIWatchSample, path string, eventLimit int) {
	var resolve *control.ResolveResponse
	debugGetJSON(ctx, "/v1/resolve?path="+url.QueryEscape(path)+"&include_remote_name=1", &resolve, &sample.Errors)
	if resolve != nil && len(resolve.Resolves) > 0 {
		item := resolve.Resolves[0]
		sample.PathResolve = &item
	}
	var uploads *control.UploadsResponse
	debugGetJSON(ctx, "/v1/uploads?history=1&path="+url.QueryEscape(path), &uploads, &sample.Errors)
	if uploads != nil {
		sample.PathUploads = uploads.Uploads
	}
	var staging *control.StagingResponse
	debugGetJSON(ctx, "/v1/staging?path="+url.QueryEscape(path), &staging, &sample.Errors)
	if staging != nil {
		sample.PathStaging = staging.Mounts
	}
	var consistency *control.ConsistencyResponse
	debugGetJSON(ctx, "/v1/consistency?path="+url.QueryEscape(path), &consistency, &sample.Errors)
	if consistency != nil {
		item := consistency.Report
		sample.PathConsistency = &item
	}
	var events *control.EventsResponse
	debugGetJSON(ctx, "/v1/events?level=warn&limit="+url.QueryEscape(fmt.Sprintf("%d", eventLimit))+"&path="+url.QueryEscape(path), &events, &sample.Errors)
	if events != nil {
		for _, event := range events.Events {
			sample.Events = append(sample.Events, controlEventSummary{
				ID:         event.ID,
				Time:       event.Time,
				Level:      event.Level,
				Component:  debugEventComponent(event.Message),
				Message:    event.Message,
				Suppressed: event.Suppressed,
			})
		}
	}
}

func addWatchDiagnostics(out *[]debugAIDiagnostic, report debugAIWatchReport) {
	if len(report.Samples) == 0 {
		*out = append(*out, debugAIDiagnostic{
			Severity: "error",
			Code:     "watch_no_samples",
			Message:  "watch did not collect any samples",
		})
		return
	}
	seenEvent := map[uint64]bool{}
	for _, sample := range report.Samples {
		if sample.HealthOK != nil && !*sample.HealthOK {
			*out = append(*out, debugAIDiagnostic{
				Severity: "error",
				Code:     "debug_socket_unhealthy",
				Message:  "debug socket health check returned not ok during watch",
				Evidence: map[string]any{"at": sample.At},
			})
		}
		for _, event := range sample.Events {
			if seenEvent[event.ID] {
				continue
			}
			seenEvent[event.ID] = true
			sev := strings.ToLower(event.Level)
			if sev != "error" && sev != "warn" {
				sev = "warn"
			}
			*out = append(*out, debugAIDiagnostic{
				Severity:  sev,
				Code:      "watch_event",
				Component: event.Component,
				Message:   event.Message,
				Evidence:  map[string]any{"event_id": event.ID, "time": event.Time, "suppressed": event.Suppressed},
			})
		}
		for _, upload := range sample.Uploads {
			if upload.LastError == "" {
				continue
			}
			*out = append(*out, debugAIDiagnostic{
				Severity:  "error",
				Code:      "watch_upload_error",
				Component: "vfs",
				Path:      upload.Path,
				Message:   upload.LastError,
				Evidence:  map[string]any{"at": sample.At, "state": upload.State, "retry_count": upload.RetryCount},
			})
		}
		if sample.PathConsistency != nil {
			status := sample.PathConsistency.Status
			if status != "" && status != "ok" && status != "uploaded_pending_cleanup" && status != "namespace_root" {
				*out = append(*out, debugAIDiagnostic{
					Severity:  "warn",
					Code:      "watch_path_consistency_issue",
					Component: "vfs",
					Path:      sample.PathConsistency.Path,
					Message:   firstNonEmpty(sample.PathConsistency.Issue, "path consistency check is not ok during watch"),
					Evidence:  map[string]any{"at": sample.At, "status": status},
				})
			}
		}
	}
	addWatchTransitionDiagnostics(out, report)
}

func addWatchTransitionDiagnostics(out *[]debugAIDiagnostic, report debugAIWatchReport) {
	type uploadKey struct {
		Path  string
		State string
	}
	seenUploadStates := map[uploadKey]bool{}
	for _, sample := range report.Samples {
		for _, upload := range append(append([]vfs.DebugUpload{}, sample.Uploads...), sample.PathUploads...) {
			if upload.Path == "" || upload.State == "" {
				continue
			}
			seenUploadStates[uploadKey{Path: upload.Path, State: upload.State}] = true
		}
	}
	paths := map[string][]string{}
	for key := range seenUploadStates {
		paths[key.Path] = append(paths[key.Path], key.State)
	}
	for path, states := range paths {
		sort.Strings(states)
		*out = append(*out, debugAIDiagnostic{
			Severity:  "info",
			Code:      "watch_upload_states",
			Component: "vfs",
			Path:      path,
			Message:   "upload states observed during watch",
			Evidence:  map[string]any{"states": states},
		})
	}
}
