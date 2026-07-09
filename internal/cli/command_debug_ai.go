package cli

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const debugAIReportSchemaVersion = 1

type debugAIReport struct {
	SchemaVersion int                          `json:"schema_version"`
	GeneratedAt   time.Time                    `json:"generated_at"`
	Command       string                       `json:"command"`
	Socket        string                       `json:"socket,omitempty"`
	Path          string                       `json:"path,omitempty"`
	Health        *control.HealthResponse      `json:"health,omitempty"`
	Runtime       *control.RuntimeResponse     `json:"runtime,omitempty"`
	State         *vfs.DebugSnapshot           `json:"state,omitempty"`
	Drivers       *control.DriversResponse     `json:"drivers,omitempty"`
	MountHealth   *control.MountHealthResponse `json:"mount_health,omitempty"`
	Events        *control.EventsResponse      `json:"events,omitempty"`
	Uploads       *control.UploadsResponse     `json:"uploads,omitempty"`
	Cache         *control.CacheResponse       `json:"cache,omitempty"`
	Staging       *control.StagingResponse     `json:"staging,omitempty"`
	Inspect       *debugAIInspect              `json:"inspect,omitempty"`
	Diagnostics   []debugAIDiagnostic          `json:"diagnostics"`
	Errors        []debugAIError               `json:"errors,omitempty"`
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
		Use:   "collect [REMOTE]",
		Short: "Collect AI-oriented diagnostic JSON",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			eventLimit, err := nonNegativeIntFlag(cmd, "events-limit")
			if err != nil {
				return err
			}
			includeMountHealth, _ := cmd.Flags().GetBool("mount-health")
			report := collectDebugAIReport(cmd.Context(), "collect", cleanDebugPath(path), eventLimit, includeMountHealth)
			return writePrettyJSON(cmd.OutOrStdout(), report)
		},
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Int("events-limit", 200, "maximum recent warn/error events")
	cmd.Flags().Bool("mount-health", false, "include runtime mount health")
	return cmd
}

func newDebugInspectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "inspect REMOTE",
		Short:             "Collect AI-oriented diagnostics for one path",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: noFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			eventLimit, err := nonNegativeIntFlag(cmd, "events-limit")
			if err != nil {
				return err
			}
			remoteName, _ := cmd.Flags().GetBool("remote-name")
			report := newDebugAIReport(cmd.Context(), "inspect", cleanDebugPath(args[0]))
			report.Inspect = debugAIInspectPath(cmd.Context(), report.Path, eventLimit, remoteName, &report.Errors)
			addInspectDiagnostics(&report.Diagnostics, report.Inspect)
			return writePrettyJSON(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().Int("events-limit", 100, "maximum recent warn/error events for the path")
	cmd.Flags().Bool("remote-name", false, "include encrypted/remote name when supported")
	return cmd
}

func newDebugWatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch [REMOTE]",
		Short: "Sample debug state during a reproduction window",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			duration, _ := cmd.Flags().GetDuration("duration")
			interval, _ := cmd.Flags().GetDuration("interval")
			eventLimit, err := nonNegativeIntFlag(cmd, "events-limit")
			if err != nil {
				return err
			}
			if err := validateSamplingWindow(duration, interval, "duration", "interval"); err != nil {
				return err
			}
			report := watchDebugAI(cmd.Context(), cleanDebugPath(path), duration, interval, eventLimit)
			return writePrettyJSON(cmd.OutOrStdout(), report)
		},
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Duration("duration", 30*time.Second, "sampling window")
	cmd.Flags().Duration("interval", 2*time.Second, "sampling interval")
	cmd.Flags().Int("events-limit", 100, "maximum recent warn/error events per sample")
	return cmd
}

func collectDebugAIReport(ctx context.Context, command, path string, eventLimit int, includeMountHealth bool) debugAIReport {
	report := newDebugAIReport(ctx, command, path)
	debugGetJSON(ctx, "/v1/health", &report.Health, &report.Errors)
	debugGetJSON(ctx, "/v1/runtime", &report.Runtime, &report.Errors)
	debugGetJSON(ctx, "/v1/state", &report.State, &report.Errors)
	debugGetJSON(ctx, "/v1/driver", &report.Drivers, &report.Errors)
	if includeMountHealth {
		debugGetJSON(ctx, "/v1/mounts/health", &report.MountHealth, &report.Errors)
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
