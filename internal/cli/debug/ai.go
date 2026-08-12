package debug

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

const DebugAIReportSchemaVersion = 2

type DebugAIReport struct {
	SchemaVersion   int                              `json:"schema_version"`
	GeneratedAt     time.Time                        `json:"generated_at"`
	Command         string                           `json:"command"`
	Socket          string                           `json:"socket,omitempty"`
	MountNames      []string                         `json:"mount_names,omitempty"`
	AllMounts       bool                             `json:"all_mounts,omitempty"`
	Path            string                           `json:"path,omitempty"`
	DestinationPath string                           `json:"destination_path,omitempty"`
	Health          *control.HealthResponse          `json:"health,omitempty"`
	Runtime         *control.RuntimeResponse         `json:"runtime,omitempty"`
	State           *diagnostics.DebugSnapshot       `json:"state,omitempty"`
	Drivers         *control.DriversResponse         `json:"drivers,omitempty"`
	MountHealth     *control.MountHealthResponse     `json:"mount_health,omitempty"`
	Events          *control.EventsResponse          `json:"events,omitempty"`
	Uploads         *control.UploadsResponse         `json:"uploads,omitempty"`
	Reads           *control.ReadsResponse           `json:"reads,omitempty"`
	Cache           *control.CacheResponse           `json:"cache,omitempty"`
	Staging         *control.StagingResponse         `json:"staging,omitempty"`
	Inspect         *DebugAIInspect                  `json:"inspect,omitempty"`
	Destination     *DebugAIInspect                  `json:"destination,omitempty"`
	TransferContext *control.TransferContextResponse `json:"transfer_context,omitempty"`
	Diagnostics     []DebugAIDiagnostic              `json:"diagnostics"`
	Errors          []DebugAIError                   `json:"errors,omitempty"`
}

type DebugAIInspect struct {
	Path        string                       `json:"path"`
	Resolve     *control.ResolveResponse     `json:"resolve,omitempty"`
	Cache       *control.CacheResponse       `json:"cache,omitempty"`
	Staging     *control.StagingResponse     `json:"staging,omitempty"`
	Uploads     *control.UploadsResponse     `json:"uploads,omitempty"`
	Reads       *control.ReadsResponse       `json:"reads,omitempty"`
	Consistency *control.ConsistencyResponse `json:"consistency,omitempty"`
	Events      *control.EventsResponse      `json:"events,omitempty"`
}

type DebugAIDiagnostic struct {
	Severity  string         `json:"severity"`
	Code      string         `json:"code"`
	Component string         `json:"component,omitempty"`
	Path      string         `json:"path,omitempty"`
	Mount     string         `json:"mount,omitempty"`
	Message   string         `json:"message"`
	Evidence  map[string]any `json:"evidence,omitempty"`
}

type DebugAIError struct {
	Endpoint string `json:"endpoint"`
	Message  string `json:"message"`
}

type DebugAIWatchReport struct {
	SchemaVersion int                  `json:"schema_version"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Command       string               `json:"command"`
	Socket        string               `json:"socket,omitempty"`
	MountNames    []string             `json:"mount_names,omitempty"`
	AllMounts     bool                 `json:"all_mounts,omitempty"`
	Path          string               `json:"path,omitempty"`
	StartedAt     time.Time            `json:"started_at"`
	EndedAt       time.Time            `json:"ended_at"`
	Duration      string               `json:"duration"`
	Interval      string               `json:"interval"`
	Samples       []DebugAIWatchSample `json:"samples"`
	Diagnostics   []DebugAIDiagnostic  `json:"diagnostics"`
	Errors        []DebugAIError       `json:"errors,omitempty"`
}

type DebugAIWatchSample struct {
	At              time.Time                       `json:"at"`
	HealthOK        *bool                           `json:"health_ok,omitempty"`
	Mounts          []DebugAIWatchMount             `json:"mounts,omitempty"`
	Uploads         []upload.UploadSnapshot         `json:"uploads,omitempty"`
	Events          []ControlEventSummary           `json:"events,omitempty"`
	Path            string                          `json:"path,omitempty"`
	PathResolve     *diagnostics.DebugResolveInfo   `json:"path_resolve,omitempty"`
	PathUploads     []upload.UploadSnapshot         `json:"path_uploads,omitempty"`
	PathStaging     []diagnostics.DebugStagingMount `json:"path_staging,omitempty"`
	PathConsistency *diagnostics.ConsistencyReport  `json:"path_consistency,omitempty"`
	Errors          []DebugAIError                  `json:"errors,omitempty"`
}

type DebugAIWatchMount struct {
	Name                      string `json:"name"`
	Driver                    string `json:"driver,omitempty"`
	Encrypted                 bool   `json:"encrypted"`
	PendingUploads            int    `json:"pending_uploads"`
	ActiveUploads             int    `json:"active_uploads"`
	StagingFiles              int    `json:"staging_files"`
	StagingOrphans            int    `json:"staging_orphans"`
	ReadCacheFiles            int    `json:"read_cache_files"`
	ReadCacheBytes            int64  `json:"read_cache_bytes"`
	ReadCacheHits             int64  `json:"read_cache_hits"`
	ReadCacheMisses           int64  `json:"read_cache_misses"`
	JournalEntries            int    `json:"journal_entries,omitempty"`
	JournalBytes              int64  `json:"journal_bytes,omitempty"`
	JournalDuplicateEntries   int    `json:"journal_duplicate_entries,omitempty"`
	JournalCompactRecommended bool   `json:"journal_compact_recommended,omitempty"`
	LastCacheGetErr           string `json:"last_cache_get_error,omitempty"`
	LastCachePutErr           string `json:"last_cache_put_error,omitempty"`
}

type ControlEventSummary struct {
	ID         uint64    `json:"id"`
	Time       time.Time `json:"time"`
	Level      string    `json:"level"`
	Component  string    `json:"component,omitempty"`
	Message    string    `json:"message"`
	Suppressed int       `json:"suppressed,omitempty"`
}

func NewCollectCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "collect [REMOTE]",
		Short: "Collect AI-oriented diagnostic JSON",
		Args:  cliruntime.MaxArgs(rt, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			eventLimit, err := cliruntime.NonNegativeIntFlag(cmd, "events-limit")
			if err != nil {
				return err
			}
			dest, _ := cmd.Flags().GetString("dest")
			mounts, allMounts, err := debugMountScopeFromFlags(rt, cmd)
			if err != nil {
				return err
			}
			report := CollectDebugAIReport(cmd.Context(), "collect", CleanDebugPath(path), CleanDebugPath(dest), eventLimit, mounts, allMounts)
			return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), report)
		},
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Int("events-limit", 200, "maximum recent warn/error events")
	cmd.Flags().String("dest", "", "optional destination path for transfer diagnostics")
	addDebugMountScopeFlags(cmd)
	return cmd
}

func NewWatchCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch [REMOTE]",
		Short: "Sample debug state during a reproduction window",
		Args:  cliruntime.MaxArgs(rt, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			duration, _ := cmd.Flags().GetDuration("duration")
			interval, _ := cmd.Flags().GetDuration("interval")
			eventLimit, err := cliruntime.NonNegativeIntFlag(cmd, "events-limit")
			if err != nil {
				return err
			}
			if err := cliruntime.ValidateSamplingWindow(duration, interval, "duration", "interval"); err != nil {
				return err
			}
			mounts, allMounts, err := debugMountScopeFromFlags(rt, cmd)
			if err != nil {
				return err
			}
			asJSONL, _ := cmd.Flags().GetBool("jsonl")
			if asJSONL {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetEscapeHTML(false)
				WatchDebugAI(cmd.Context(), CleanDebugPath(path), duration, interval, eventLimit, mounts, allMounts, func(sample DebugAIWatchSample) {
					_ = encoder.Encode(sample)
				})
				return nil
			}
			report := WatchDebugAI(cmd.Context(), CleanDebugPath(path), duration, interval, eventLimit, mounts, allMounts, nil)
			return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), report)
		},
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Duration("duration", 30*time.Second, "sampling window")
	cmd.Flags().Duration("interval", 2*time.Second, "sampling interval")
	cmd.Flags().Int("events-limit", 100, "maximum recent warn/error events per sample")
	cmd.Flags().Bool("jsonl", false, "stream one JSON sample per line instead of a single report")
	addDebugMountScopeFlags(cmd)
	return cmd
}

func CollectDebugAIReport(ctx context.Context, command, path, destinationPath string, eventLimit int, mountNames []string, allMounts bool) DebugAIReport {
	report := NewDebugAIReport(ctx, command, path)
	report.MountNames = mountNames
	report.AllMounts = allMounts
	report.DestinationPath = destinationPath
	debugGetJSON(ctx, "/v1/health", &report.Health, &report.Errors)
	debugGetJSON(ctx, "/v1/runtime", &report.Runtime, &report.Errors)
	debugGetJSON(ctx, debugEndpointWithMounts("/v1/state", mountNames), &report.State, &report.Errors)
	debugGetJSON(ctx, debugEndpointWithMounts("/v1/driver", mountNames), &report.Drivers, &report.Errors)
	debugGetJSON(ctx, debugEndpointWithMounts("/v1/mounts/health", mountNames), &report.MountHealth, &report.Errors)
	debugGetJSON(ctx, "/v1/events?level=warn&limit="+url.QueryEscape(fmt.Sprintf("%d", eventLimit)), &report.Events, &report.Errors)
	debugGetJSON(ctx, debugEndpointWithMounts("/v1/uploads?history=1", mountNames), &report.Uploads, &report.Errors)
	debugGetJSON(ctx, debugEndpointWithMounts("/v1/reads", mountNames), &report.Reads, &report.Errors)
	debugGetJSON(ctx, debugEndpointWithMounts("/v1/cache", mountNames), &report.Cache, &report.Errors)
	debugGetJSON(ctx, debugEndpointWithMounts("/v1/staging", mountNames), &report.Staging, &report.Errors)
	if path != "" {
		report.Inspect = DebugAIInspectPath(ctx, path, eventLimit, mountNames, &report.Errors)
	}
	if destinationPath != "" {
		report.Destination = DebugAIInspectPath(ctx, destinationPath, eventLimit, mountNames, &report.Errors)
	}
	if path != "" && destinationPath != "" {
		debugGetJSON(ctx, transferContextEndpoint(path, destinationPath), &report.TransferContext, &report.Errors)
	}
	AddCollectDiagnostics(&report.Diagnostics, report)
	if report.Inspect != nil {
		addInspectDiagnostics(&report.Diagnostics, report.Inspect)
	}
	if report.Destination != nil {
		addInspectDiagnostics(&report.Diagnostics, report.Destination)
	}
	return report
}

func transferContextEndpoint(source, dest string) string {
	return "/v1/transfer/context?source=" + url.QueryEscape(source) + "&dest=" + url.QueryEscape(dest)
}

func addDebugMountScopeFlags(cmd *cobra.Command) {
	cmd.Flags().StringArray("mount", nil, "mount name to inspect (repeatable)")
	cmd.Flags().Bool("all-mounts", false, "inspect all mounts")
}

func debugMountScopeFromFlags(rt cliruntime.Runtime, cmd *cobra.Command) ([]string, bool, error) {
	mounts, err := cmd.Flags().GetStringArray("mount")
	if err != nil {
		return nil, false, err
	}
	allMounts, err := cmd.Flags().GetBool("all-mounts")
	if err != nil {
		return nil, false, err
	}
	mounts = cleanDebugMountNames(mounts)
	if len(mounts) > 0 && allMounts {
		return nil, false, fmt.Errorf("--mount and --all-mounts cannot be used together")
	}
	if len(mounts) == 0 && !allMounts {
		return nil, false, rt.UsageError(cmd, "specify --mount NAME or --all-mounts")
	}
	return mounts, allMounts, nil
}

func cleanDebugMountNames(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		name := strings.Trim(strings.TrimSpace(value), "/")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func debugEndpointWithMounts(endpoint string, mountNames []string) string {
	if len(mountNames) == 0 {
		return endpoint
	}
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	var b strings.Builder
	b.WriteString(endpoint)
	for i, mount := range mountNames {
		if i == 0 {
			b.WriteString(sep)
		} else {
			b.WriteByte('&')
		}
		b.WriteString("mount=")
		b.WriteString(url.QueryEscape(mount))
	}
	return b.String()
}
