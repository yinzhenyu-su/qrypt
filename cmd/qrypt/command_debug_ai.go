package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const debugAIReportSchemaVersion = 1

type debugAIReport struct {
	SchemaVersion int                           `json:"schema_version"`
	GeneratedAt   time.Time                     `json:"generated_at"`
	Command       string                        `json:"command"`
	Socket        string                        `json:"socket,omitempty"`
	Path          string                        `json:"path,omitempty"`
	Health        *control.HealthResponse       `json:"health,omitempty"`
	Runtime       *control.RuntimeResponse      `json:"runtime,omitempty"`
	State         *vfs.DebugSnapshot            `json:"state,omitempty"`
	Drivers       *control.DriversResponse      `json:"drivers,omitempty"`
	DriverHealth  *control.DriverHealthResponse `json:"driver_health,omitempty"`
	Events        *control.EventsResponse       `json:"events,omitempty"`
	Uploads       *control.UploadsResponse      `json:"uploads,omitempty"`
	Cache         *control.CacheResponse        `json:"cache,omitempty"`
	Staging       *control.StagingResponse      `json:"staging,omitempty"`
	Inspect       *debugAIInspect               `json:"inspect,omitempty"`
	Diagnostics   []debugAIDiagnostic           `json:"diagnostics"`
	Errors        []debugAIError                `json:"errors,omitempty"`
}

type debugAIInspect struct {
	Path        string                       `json:"path"`
	Resolve     *control.ResolveResponse     `json:"resolve,omitempty"`
	Cache       *control.CacheResponse       `json:"cache,omitempty"`
	Staging     *control.StagingResponse     `json:"staging,omitempty"`
	Uploads     *control.UploadsResponse     `json:"uploads,omitempty"`
	Consistency *control.ConsistencyResponse `json:"consistency,omitempty"`
	Events      *control.EventsResponse      `json:"events,omitempty"`
}

type debugAIDiagnostic struct {
	Severity  string         `json:"severity"`
	Code      string         `json:"code"`
	Component string         `json:"component,omitempty"`
	Path      string         `json:"path,omitempty"`
	Mount     string         `json:"mount,omitempty"`
	Message   string         `json:"message"`
	Evidence  map[string]any `json:"evidence,omitempty"`
}

type debugAIError struct {
	Endpoint string `json:"endpoint"`
	Message  string `json:"message"`
}

type debugAIWatchReport struct {
	SchemaVersion int                  `json:"schema_version"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Command       string               `json:"command"`
	Socket        string               `json:"socket,omitempty"`
	Path          string               `json:"path,omitempty"`
	StartedAt     time.Time            `json:"started_at"`
	EndedAt       time.Time            `json:"ended_at"`
	Duration      string               `json:"duration"`
	Interval      string               `json:"interval"`
	Samples       []debugAIWatchSample `json:"samples"`
	Diagnostics   []debugAIDiagnostic  `json:"diagnostics"`
	Errors        []debugAIError       `json:"errors,omitempty"`
}

type debugAIWatchSample struct {
	At              time.Time               `json:"at"`
	HealthOK        *bool                   `json:"health_ok,omitempty"`
	Mounts          []debugAIWatchMount     `json:"mounts,omitempty"`
	Uploads         []vfs.DebugUpload       `json:"uploads,omitempty"`
	Events          []controlEventSummary   `json:"events,omitempty"`
	Path            string                  `json:"path,omitempty"`
	PathResolve     *vfs.DebugResolveInfo   `json:"path_resolve,omitempty"`
	PathUploads     []vfs.DebugUpload       `json:"path_uploads,omitempty"`
	PathStaging     []vfs.DebugStagingMount `json:"path_staging,omitempty"`
	PathConsistency *vfs.ConsistencyReport  `json:"path_consistency,omitempty"`
	Errors          []debugAIError          `json:"errors,omitempty"`
}

type debugAIWatchMount struct {
	Name            string `json:"name"`
	Driver          string `json:"driver,omitempty"`
	Encrypted       bool   `json:"encrypted"`
	PendingUploads  int    `json:"pending_uploads"`
	ActiveUploads   int    `json:"active_uploads"`
	StagingFiles    int    `json:"staging_files"`
	StagingOrphans  int    `json:"staging_orphans"`
	ReadCacheFiles  int    `json:"read_cache_files"`
	ReadCacheBytes  int64  `json:"read_cache_bytes"`
	ReadCacheHits   int64  `json:"read_cache_hits"`
	ReadCacheMisses int64  `json:"read_cache_misses"`
	LastCacheGetErr string `json:"last_cache_get_error,omitempty"`
	LastCachePutErr string `json:"last_cache_put_error,omitempty"`
}

type controlEventSummary struct {
	ID         uint64    `json:"id"`
	Time       time.Time `json:"time"`
	Level      string    `json:"level"`
	Component  string    `json:"component,omitempty"`
	Message    string    `json:"message"`
	Suppressed int       `json:"suppressed,omitempty"`
}

func newDebugCollectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "collect",
		Short: "Collect AI-oriented diagnostic JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _ := cmd.Flags().GetString("path")
			eventLimit, _ := cmd.Flags().GetInt("events-limit")
			includeDriverHealth, _ := cmd.Flags().GetBool("driver-health")
			report := collectDebugAIReport(context.Background(), "collect", cleanDebugPath(path), eventLimit, includeDriverHealth)
			return writeDebugAIReport(report)
		},
	}
	cmd.Flags().String("path", "", "optional path to inspect in the same report")
	cmd.Flags().Int("events-limit", 200, "maximum recent warn/error events")
	cmd.Flags().Bool("driver-health", false, "run live driver health checks")
	return cmd
}

func newDebugInspectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect PATH",
		Short: "Collect AI-oriented diagnostics for one path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eventLimit, _ := cmd.Flags().GetInt("events-limit")
			remoteName, _ := cmd.Flags().GetBool("remote-name")
			report := newDebugAIReport("inspect", cleanDebugPath(args[0]))
			report.Inspect = debugAIInspectPath(context.Background(), report.Path, eventLimit, remoteName, &report.Errors)
			addInspectDiagnostics(&report.Diagnostics, report.Inspect)
			return writeDebugAIReport(report)
		},
	}
	cmd.Flags().Int("events-limit", 100, "maximum recent warn/error events for the path")
	cmd.Flags().Bool("remote-name", false, "include encrypted/remote name when supported")
	return cmd
}

func newDebugWatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Sample debug state during a reproduction window",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _ := cmd.Flags().GetString("path")
			duration, _ := cmd.Flags().GetDuration("duration")
			interval, _ := cmd.Flags().GetDuration("interval")
			eventLimit, _ := cmd.Flags().GetInt("events-limit")
			if duration <= 0 {
				return fmt.Errorf("--duration must be greater than 0")
			}
			if interval <= 0 {
				return fmt.Errorf("--interval must be greater than 0")
			}
			report := watchDebugAI(context.Background(), cleanDebugPath(path), duration, interval, eventLimit)
			return writeDebugAIWatchReport(report)
		},
	}
	cmd.Flags().String("path", "", "optional path to inspect in each sample")
	cmd.Flags().Duration("duration", 30*time.Second, "sampling window")
	cmd.Flags().Duration("interval", 2*time.Second, "sampling interval")
	cmd.Flags().Int("events-limit", 100, "maximum recent warn/error events per sample")
	return cmd
}

func collectDebugAIReport(ctx context.Context, command, path string, eventLimit int, includeDriverHealth bool) debugAIReport {
	report := newDebugAIReport(command, path)
	debugGetJSON(ctx, "/v1/health", &report.Health, &report.Errors)
	debugGetJSON(ctx, "/v1/runtime", &report.Runtime, &report.Errors)
	debugGetJSON(ctx, "/v1/state", &report.State, &report.Errors)
	debugGetJSON(ctx, "/v1/driver", &report.Drivers, &report.Errors)
	if includeDriverHealth {
		debugGetJSON(ctx, "/v1/driver?health=true", &report.DriverHealth, &report.Errors)
	}
	debugGetJSON(ctx, "/v1/events?level=warn&limit="+url.QueryEscape(fmt.Sprintf("%d", eventLimit)), &report.Events, &report.Errors)
	debugGetJSON(ctx, "/v1/uploads?history=1", &report.Uploads, &report.Errors)
	debugGetJSON(ctx, "/v1/cache", &report.Cache, &report.Errors)
	debugGetJSON(ctx, "/v1/staging", &report.Staging, &report.Errors)
	if path != "" {
		report.Inspect = debugAIInspectPath(ctx, path, eventLimit, true, &report.Errors)
	}
	addCollectDiagnostics(&report.Diagnostics, report)
	if report.Inspect != nil {
		addInspectDiagnostics(&report.Diagnostics, report.Inspect)
	}
	return report
}

func watchDebugAI(ctx context.Context, path string, duration, interval time.Duration, eventLimit int) debugAIWatchReport {
	startedAt := time.Now()
	report := debugAIWatchReport{
		SchemaVersion: debugAIReportSchemaVersion,
		GeneratedAt:   startedAt,
		Command:       "watch",
		Socket:        debugSocket,
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

func newDebugAIReport(command, path string) debugAIReport {
	return debugAIReport{
		SchemaVersion: debugAIReportSchemaVersion,
		GeneratedAt:   time.Now(),
		Command:       command,
		Socket:        debugSocket,
		Path:          path,
		Diagnostics:   []debugAIDiagnostic{},
	}
}

func debugAIInspectPath(ctx context.Context, path string, eventLimit int, remoteName bool, errors *[]debugAIError) *debugAIInspect {
	inspect := &debugAIInspect{Path: path}
	resolveEndpoint := "/v1/resolve?path=" + url.QueryEscape(path)
	if remoteName {
		resolveEndpoint += "&include_remote_name=1"
	}
	debugGetJSON(ctx, resolveEndpoint, &inspect.Resolve, errors)
	if path != "/" {
		debugGetJSON(ctx, "/v1/cache?path="+url.QueryEscape(path), &inspect.Cache, errors)
	}
	debugGetJSON(ctx, "/v1/staging?path="+url.QueryEscape(path), &inspect.Staging, errors)
	debugGetJSON(ctx, "/v1/uploads?history=1&path="+url.QueryEscape(path), &inspect.Uploads, errors)
	debugGetJSON(ctx, "/v1/consistency?path="+url.QueryEscape(path), &inspect.Consistency, errors)
	debugGetJSON(ctx, "/v1/events?level=warn&limit="+url.QueryEscape(fmt.Sprintf("%d", eventLimit))+"&path="+url.QueryEscape(path), &inspect.Events, errors)
	return inspect
}

func debugGetJSON[T any](ctx context.Context, endpoint string, target **T, errors *[]debugAIError) {
	c := debugSocketClient{}
	body, err := c.get(endpoint)
	if err != nil {
		*errors = append(*errors, debugAIError{Endpoint: endpoint, Message: err.Error()})
		return
	}
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		*errors = append(*errors, debugAIError{Endpoint: endpoint, Message: err.Error()})
		return
	}
	*target = &value
	_ = ctx
}

func addCollectDiagnostics(out *[]debugAIDiagnostic, report debugAIReport) {
	if report.Health == nil || !report.Health.OK {
		*out = append(*out, debugAIDiagnostic{
			Severity: "error",
			Code:     "debug_socket_unhealthy",
			Message:  "debug socket health check failed or did not return ok",
		})
	}
	if report.State != nil {
		for _, mount := range report.State.Mounts {
			if len(mount.Pending) > 0 {
				*out = append(*out, debugAIDiagnostic{
					Severity:  "warn",
					Code:      "pending_uploads",
					Component: "vfs",
					Mount:     mount.Name,
					Message:   "mount has pending uploads",
					Evidence:  map[string]any{"count": len(mount.Pending)},
				})
			}
			for _, upload := range mount.Uploads {
				if upload.LastError == "" {
					continue
				}
				*out = append(*out, debugAIDiagnostic{
					Severity:  "error",
					Code:      "upload_error",
					Component: "vfs",
					Mount:     mount.Name,
					Path:      prefixDebugMountPath(report.State.Kind, mount.Name, upload.Path),
					Message:   upload.LastError,
					Evidence:  map[string]any{"state": upload.State, "retry_count": upload.RetryCount},
				})
			}
		}
	}
	if report.DriverHealth != nil {
		for _, h := range report.DriverHealth.Drivers {
			if h.OK {
				continue
			}
			*out = append(*out, debugAIDiagnostic{
				Severity:  "error",
				Code:      "driver_health_failed",
				Component: "driver",
				Mount:     h.Mount,
				Message:   firstNonEmpty(h.Error, "driver health check failed"),
				Evidence:  map[string]any{"driver": h.Driver, "latency": h.Latency},
			})
		}
	}
	if report.Events != nil {
		for _, event := range report.Events.Events {
			sev := strings.ToLower(event.Level)
			if sev != "error" && sev != "warn" {
				sev = "warn"
			}
			*out = append(*out, debugAIDiagnostic{
				Severity:  sev,
				Code:      "recent_event",
				Component: debugEventComponent(event.Message),
				Message:   event.Message,
				Evidence:  map[string]any{"event_id": event.ID, "time": event.Time, "suppressed": event.Suppressed},
			})
		}
	}
	addStagingDiagnostics(out, report.Staging)
	addCacheDiagnostics(out, report.Cache)
}

func addInspectDiagnostics(out *[]debugAIDiagnostic, inspect *debugAIInspect) {
	if inspect == nil {
		return
	}
	if inspect.Resolve == nil || len(inspect.Resolve.Resolves) == 0 {
		*out = append(*out, debugAIDiagnostic{
			Severity:  "error",
			Code:      "path_resolve_failed",
			Component: "vfs",
			Path:      inspect.Path,
			Message:   "path could not be resolved",
		})
	}
	if inspect.Consistency != nil {
		report := inspect.Consistency.Report
		if report.Status != "" && report.Status != "ok" && report.Status != "uploaded_pending_cleanup" && report.Status != "namespace_root" {
			*out = append(*out, debugAIDiagnostic{
				Severity:  "warn",
				Code:      "path_consistency_issue",
				Component: "vfs",
				Path:      report.Path,
				Message:   firstNonEmpty(report.Issue, "path consistency check is not ok"),
				Evidence:  map[string]any{"status": report.Status, "pending": report.Pending, "remote_found": report.RemoteFound, "size_matches": report.SizeMatches},
			})
		}
	}
	if inspect.Uploads != nil {
		for _, upload := range inspect.Uploads.Uploads {
			if upload.LastError == "" {
				continue
			}
			*out = append(*out, debugAIDiagnostic{
				Severity:  "error",
				Code:      "path_upload_error",
				Component: "vfs",
				Path:      upload.Path,
				Message:   upload.LastError,
				Evidence:  map[string]any{"state": upload.State, "retry_count": upload.RetryCount},
			})
		}
	}
	addStagingDiagnostics(out, inspect.Staging)
	addCacheDiagnostics(out, inspect.Cache)
}

func addStagingDiagnostics(out *[]debugAIDiagnostic, staging *control.StagingResponse) {
	if staging == nil {
		return
	}
	for _, mount := range staging.Mounts {
		for _, file := range mount.Files {
			hasIssue := file.Issue != "" || file.Pending && !file.Exists || file.Pending && file.Exists && !file.SizeMatches
			if !hasIssue {
				continue
			}
			code := "staging_issue"
			if file.Pending && !file.Exists {
				code = "staging_missing"
			} else if file.Exists && !file.SizeMatches {
				code = "staging_size_mismatch"
			}
			*out = append(*out, debugAIDiagnostic{
				Severity:  "warn",
				Code:      code,
				Component: "staging",
				Mount:     mount.Mount,
				Path:      file.Path,
				Message:   firstNonEmpty(file.Issue, "staging file state is inconsistent"),
				Evidence:  map[string]any{"local_path": file.LocalPath, "pending_size": file.PendingSize, "staging_size": file.StagingSize, "upload_in_progress": file.UploadInProgress},
			})
		}
		for _, file := range mount.Orphans {
			*out = append(*out, debugAIDiagnostic{
				Severity:  "warn",
				Code:      "staging_orphan",
				Component: "staging",
				Mount:     mount.Mount,
				Path:      file.Path,
				Message:   firstNonEmpty(file.Issue, "staging file is not referenced by pending upload state"),
				Evidence:  map[string]any{"local_path": file.LocalPath, "staging_size": file.StagingSize},
			})
		}
	}
}

func addCacheDiagnostics(out *[]debugAIDiagnostic, cache *control.CacheResponse) {
	if cache == nil {
		return
	}
	for _, mount := range cache.Mounts {
		if mount.Cache.LastGetError != "" {
			*out = append(*out, debugAIDiagnostic{
				Severity:  "warn",
				Code:      "read_cache_get_error",
				Component: "cache",
				Mount:     mount.Mount,
				Path:      cache.Path,
				Message:   mount.Cache.LastGetError,
				Evidence:  map[string]any{"at": mount.Cache.LastGetErrorAt},
			})
		}
		if mount.Cache.LastPutError != "" {
			*out = append(*out, debugAIDiagnostic{
				Severity:  "warn",
				Code:      "read_cache_put_error",
				Component: "cache",
				Mount:     mount.Mount,
				Path:      cache.Path,
				Message:   mount.Cache.LastPutError,
				Evidence:  map[string]any{"at": mount.Cache.LastPutErrorAt},
			})
		}
	}
}

func writeDebugAIReport(report debugAIReport) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func writeDebugAIWatchReport(report debugAIWatchReport) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func cleanDebugPath(path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean("/" + strings.TrimPrefix(path, "/"))
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func prefixDebugMountPath(kind, mountName, path string) string {
	if kind != "namespace" || mountName == "" {
		return cleanDebugPath(path)
	}
	return cleanDebugPath("/" + strings.Trim(mountName, "/") + "/" + strings.TrimPrefix(path, "/"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func debugEventComponent(message string) string {
	if !strings.HasPrefix(message, "[") {
		return ""
	}
	end := strings.Index(message, "]")
	if end <= 1 {
		return ""
	}
	return strings.ToLower(message[1:end])
}
