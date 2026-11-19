package debug

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/contracttest"
	"github.com/yinzhenyu/qrypt/pkg/control"
	vfsread "github.com/yinzhenyu/qrypt/pkg/vfs/read"

	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// NewVerifyCmd groups checks that compare a requested read policy with the
// shape the read layer actually executed. A mismatch is a code or config
// finding, not a benchmark result, so it exits nonzero.
func NewVerifyCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify that the executed read shape matches the requested policy",
		Long: `Verify that a read executed by the running mount used the requested
read-policy shape: window size, stream fan-out, part size, and prefetch depth.
Unlike the benchmark commands, a mismatch exits nonzero so a test can gate a
read policy change.`,
		Example: `  qrypt probe verify read --mount cloud --path /cloud/videos/movie.mp4
  qrypt probe verify read --mount cloud --path /cloud/videos/movie.mp4 --window 8MiB --streams 4`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	cmd.AddCommand(withProbeSocketFlag(rt, newVerifyReadCmd(rt)))
	return cmd
}

type verifyReadReport struct {
	SchemaVersion int                                   `json:"schema_version"`
	Command       string                                `json:"command"`
	GeneratedAt   time.Time                             `json:"generated_at"`
	OK            bool                                  `json:"ok"`
	Mount         string                                `json:"mount"`
	Path          string                                `json:"path"`
	MountPoint    string                                `json:"mount_point"`
	Requested     verifyReadRequested                   `json:"requested"`
	Executed      verifyReadExecuted                    `json:"executed"`
	Counters      verifyReadCounters                    `json:"counters"`
	Measurements  []contracttest.MountedReadMeasurement `json:"measurements"`
	Mismatches    []string                              `json:"mismatches"`
	Warnings      []string                              `json:"warnings"`
	Error         string                                `json:"error,omitempty"`
}

type verifyReadRequested struct {
	Offset           int64  `json:"offset"`
	Length           int64  `json:"length"`
	WindowBytes      int64  `json:"window_bytes"`
	WindowChunks     int    `json:"window_chunks"`
	Streams          int    `json:"streams"`
	PrefetchDepth    int    `json:"prefetch_depth"`
	PrefetchBytes    int64  `json:"prefetch_window_bytes"`
	PartBytes        int64  `json:"part_bytes,omitempty"`
	MinParallelBytes int64  `json:"min_parallel_bytes,omitempty"`
	BlockSize        string `json:"block_size"`
}

type verifyReadExecuted struct {
	Streams         int   `json:"streams,omitempty"`
	WindowChunks    int   `json:"window_chunks,omitempty"`
	WindowBytes     int64 `json:"window_bytes,omitempty"`
	PartBytes       int64 `json:"part_bytes,omitempty"`
	PrefetchWindows int   `json:"prefetch_windows,omitempty"`
}

type verifyReadCounters struct {
	PrefetchesBefore       int64 `json:"prefetches_before"`
	PrefetchesAfter        int64 `json:"prefetches_after"`
	PrefetchesDelta        int64 `json:"prefetches_delta"`
	WindowLoadsBefore      int64 `json:"window_loads_before"`
	WindowLoadsAfter       int64 `json:"window_loads_after"`
	WindowLoadsDelta       int64 `json:"window_loads_delta"`
	InteractiveChunksDelta int64 `json:"interactive_window_chunks_delta"`
	BulkChunksDelta        int64 `json:"bulk_window_chunks_delta"`
	ReadTimeouts           int64 `json:"read_timeouts"`
	PrefetchTimeouts       int64 `json:"prefetch_timeouts"`
}

func newVerifyReadCmd(rt cliruntime.Runtime) *cobra.Command {
	var mountPoint, virtualPath, offset, length string
	var window, prefetchWindow, partSize, minParallel, blockSize string
	var streams, prefetchDepth int
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "read",
		Short: "Verify that one read executed the requested read-policy shape",
		Long: `Read one file through the mounted VFS with an explicit read-policy
override (window, streams, prefetch depth, part size) and compare the shape the
read layer actually executed with the shape that was requested. The mount read
cache for the file's mount is cleared first so prefetch and stream scheduling
run on a cold cache.`,
		Example: `  qrypt probe verify read --mount cloud --path /cloud/videos/movie.mp4
  qrypt probe verify read --mount cloud --path /cloud/videos/movie.mp4 --window 8MiB --streams 4 --prefetch-depth 2 --length 32m --json`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVerifyRead(cmd, rt, verifyReadFlags{
				mountPoint: mountPoint, virtualPath: virtualPath, offset: offset, length: length,
				window: window, prefetchWindow: prefetchWindow, partSize: partSize,
				minParallel: minParallel, blockSize: blockSize, streams: streams,
				prefetchDepth: prefetchDepth, jsonOutput: jsonOutput,
			})
		},
	}
	cmd.Flags().String("mount", "", "mount name to verify")
	cmd.Flags().StringVar(&mountPoint, "mount-point", "", "FUSE mount point (defaults to mount_point from config)")
	cmd.Flags().StringVar(&virtualPath, "path", "", "absolute virtual path of the file to read")
	cmd.Flags().StringVar(&offset, "offset", "", "start offset within the file (default 0)")
	cmd.Flags().StringVar(&length, "length", "16m", "bytes to read (clamped to the end of the file)")
	cmd.Flags().StringVar(&window, "window", "4MiB", "requested read window (rounded up to the read-cache chunk size)")
	cmd.Flags().IntVar(&streams, "streams", 3, "requested parallel stream count")
	cmd.Flags().IntVar(&prefetchDepth, "prefetch-depth", 2, "requested prefetch depth (0 disables prefetch)")
	cmd.Flags().StringVar(&prefetchWindow, "prefetch-window", "", "requested prefetch window size (defaults to the read window)")
	cmd.Flags().StringVar(&partSize, "part-size", "", "requested per-stream part size; empty keeps the driver policy")
	cmd.Flags().StringVar(&minParallel, "min-parallel", "", "minimum window size that may split; empty keeps the driver policy")
	cmd.Flags().StringVar(&blockSize, "block-size", "1m", "FUSE read block size used to drive the read")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print a machine-readable JSON report")
	return cmd
}

const exitFailure = 1

type verifyReadFlags struct {
	mountPoint, virtualPath, offset, length string
	window, prefetchWindow, partSize        string
	minParallel, blockSize                  string
	streams, prefetchDepth                  int
	jsonOutput                              bool
}

func runVerifyRead(cmd *cobra.Command, rt cliruntime.Runtime, flags verifyReadFlags) error {
	state, err := rt.CommandConfig(cmd)
	if err != nil {
		return err
	}
	if state.Cfg == nil {
		return rt.ConfigNotFoundError()
	}
	mount := contracttest.NormalizeMountName(mustFlag(cmd, "mount"))
	if mount == "" {
		return fmt.Errorf("verify read requires --mount")
	}
	virtualPath := vfstypes.CleanVirtualPath(strings.TrimSpace(flags.virtualPath))
	if virtualPath == "" || !strings.HasPrefix(virtualPath, "/") {
		return fmt.Errorf("verify read requires --path with an absolute virtual path")
	}
	mountPoint := strings.TrimSpace(flags.mountPoint)
	if mountPoint == "" {
		mountPoint = state.Cfg.EffectiveMountPoint()
	}
	mountPoint = util.ExpandHome(strings.TrimSpace(mountPoint))
	if mountPoint == "" {
		return fmt.Errorf("verify read could not resolve the mount point; pass --mount-point")
	}
	if abs, absErr := filepath.Abs(mountPoint); absErr == nil {
		mountPoint = abs
	}
	fusePath := filepath.Join(mountPoint, strings.TrimPrefix(virtualPath, "/"))
	info, err := os.Stat(fusePath)
	if err != nil {
		return fmt.Errorf("verify read: stat %s: %w", fusePath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("verify read: %s is a directory", virtualPath)
	}

	windowBytes, err := config.ParseSize(flags.window)
	if err != nil || windowBytes <= 0 {
		return fmt.Errorf("verify read: invalid --window %q", flags.window)
	}
	prefetchBytes, err := optionalSize(flags.prefetchWindow)
	if err != nil {
		return fmt.Errorf("verify read: invalid --prefetch-window %q: %w", flags.prefetchWindow, err)
	}
	partBytes, err := optionalSize(flags.partSize)
	if err != nil {
		return fmt.Errorf("verify read: invalid --part-size %q: %w", flags.partSize, err)
	}
	minParallelBytes, err := optionalSize(flags.minParallel)
	if err != nil {
		return fmt.Errorf("verify read: invalid --min-parallel %q: %w", flags.minParallel, err)
	}
	blockBytes, err := config.ParseSize(flags.blockSize)
	if err != nil || blockBytes <= 0 || blockBytes > 16<<20 {
		return fmt.Errorf("verify read: --block-size must be between 1 byte and 16 MiB")
	}
	if flags.streams < 0 || flags.streams > 32 {
		return fmt.Errorf("verify read: --streams must be between 0 and 32")
	}
	if flags.prefetchDepth < 0 || flags.prefetchDepth > 4 {
		return fmt.Errorf("verify read: --prefetch-depth must be between 0 and 4")
	}

	size := info.Size()
	startOffset, err := parseVerifyOffset(flags.offset)
	if err != nil {
		return err
	}
	if startOffset < 0 || startOffset > size {
		return fmt.Errorf("verify read: --offset must be between 0 and %d", size)
	}
	lengthBytes := contracttest.ParseXferSize(strings.TrimSpace(flags.length))
	if lengthBytes <= 0 {
		lengthBytes = size - startOffset
	}
	var warnings []string
	if startOffset+lengthBytes > size {
		lengthBytes = size - startOffset
		warnings = append(warnings, fmt.Sprintf("length clamped to the end of the file (%d bytes available)", lengthBytes))
	}
	if lengthBytes <= 0 {
		return fmt.Errorf("verify read: the requested range is empty")
	}

	requestedWindowChunks := int((windowBytes + vfsread.ChunkSize - 1) / vfsread.ChunkSize)
	if requestedWindowChunks < 1 {
		requestedWindowChunks = 1
	}
	effectiveWindow := int64(requestedWindowChunks) * vfsread.ChunkSize

	depth := flags.prefetchDepth
	req := contracttest.DriverTestRequest{
		Test:                "read",
		Mount:               mount,
		MountPoint:          mountPoint,
		Path:                virtualPath,
		Offset:              strconv.FormatInt(startOffset, 10),
		Length:              strconv.FormatInt(lengthBytes, 10),
		Streams:             flags.streams,
		WindowBytes:         effectiveWindow,
		PrefetchWindowBytes: prefetchBytes,
		PrefetchDepth:       &depth,
		BlockSize:           strconv.FormatInt(blockBytes, 10),
		CacheMode:           "cold",
		ReadPattern:         "sequential",
		Samples:             1,
	}
	if partBytes > 0 {
		req.PartBytes = &partBytes
	}
	if minParallelBytes > 0 {
		req.MinParallelBytes = &minParallelBytes
	}
	if err := ValidateDriverTestRequest(req); err != nil {
		return fmt.Errorf("verify read: %w", err)
	}

	before, beforeErr := fetchReadMemoryMount(cmd, mount)
	if beforeErr != nil {
		warnings = append(warnings, "read-memory before the read failed: "+beforeErr.Error())
	}
	if body, clearErr := debugSocketPostJSON(cmd.Context(), "/v1/debug/cache/clear?mount="+url.QueryEscape(mount), map[string]string{}); clearErr != nil {
		if detail, routeMissing := debugSocket404Detail(clearErr); detail != "" {
			warnings = append(warnings, "read cache was not cleared: "+detail)
		} else if routeMissing {
			warnings = append(warnings, "read cache was not cleared: the mount process predates the cache-clear endpoint")
		} else {
			warnings = append(warnings, "read cache was not cleared: "+clearErr.Error())
		}
		_ = body
	}

	body, err := debugSocketPostJSON(cmd.Context(), "/v1/driver/test", req)
	if err != nil {
		if detail, routeMissing := debugSocket404Detail(err); detail != "" {
			return fmt.Errorf("verify read failed: %s", detail)
		} else if routeMissing {
			return fmt.Errorf("verify read endpoint is not available on this socket; restart the qrypt mount process with the current binary")
		}
		return err
	}
	runs, err := parseDriverTestRuns(body)
	if err != nil {
		return fmt.Errorf("verify read: decode probe result: %w", err)
	}
	report := verifyReadReport{
		SchemaVersion: diagnosticsSchemaVersion,
		Command:       "probe verify read",
		GeneratedAt:   time.Now(),
		Mount:         mount,
		Path:          virtualPath,
		MountPoint:    mountPoint,
		Requested: verifyReadRequested{
			Offset: startOffset, Length: lengthBytes,
			WindowBytes: effectiveWindow, WindowChunks: requestedWindowChunks,
			Streams: flags.streams, PrefetchDepth: depth,
			PrefetchBytes: prefetchBytes, PartBytes: partBytes,
			MinParallelBytes: minParallelBytes, BlockSize: flags.blockSize,
		},
		Warnings: warnings,
	}
	run := findReadRun(runs)
	if run == nil {
		report.Error = "probe returned no read test result"
		return finishVerifyRead(cmd, rt, &report, flags.jsonOutput)
	}
	if run.Skipped {
		report.Error = "read test was skipped: " + run.SkipReason
		return finishVerifyRead(cmd, rt, &report, flags.jsonOutput)
	}
	if !run.Pass {
		report.Error = run.Error
		if report.Error == "" {
			report.Error = "read test failed"
		}
		return finishVerifyRead(cmd, rt, &report, flags.jsonOutput)
	}
	if run.Read == nil || len(run.Read.Measurements) == 0 {
		report.Error = "read test produced no measurements"
		return finishVerifyRead(cmd, rt, &report, flags.jsonOutput)
	}
	report.Measurements = run.Read.Measurements
	measurement := run.Read.Measurements[0]
	after, afterErr := fetchReadMemoryMount(cmd, mount)
	if afterErr != nil {
		report.Warnings = append(report.Warnings, "read-memory after the read failed: "+afterErr.Error())
	}
	report.Counters = buildVerifyCounters(before, after)

	compareVerifyRead(&report, &measurement, flags.streams, depth, partBytes)
	return finishVerifyRead(cmd, rt, &report, flags.jsonOutput)
}

func compareVerifyRead(report *verifyReadReport, m *contracttest.MountedReadMeasurement, requestedStreams, prefetchDepth int, requestedPartBytes int64) {
	executed := verifyReadExecuted{
		Streams:         m.ExecutedStreams,
		WindowChunks:    m.ExecutedWindowChunks,
		WindowBytes:     m.ExecutedWindowBytes,
		PartBytes:       m.ExecutedPartBytes,
		PrefetchWindows: m.ExecutedPrefetchWindows,
	}
	report.Executed = executed
	windowLoads := report.Counters.WindowLoadsDelta + report.Counters.PrefetchesDelta
	if executed.WindowBytes > 0 {
		if executed.WindowBytes != report.Requested.WindowBytes {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf(
				"window: requested %d bytes (%d chunks of %d), executed %d bytes",
				report.Requested.WindowBytes, report.Requested.WindowChunks, vfsread.ChunkSize, executed.WindowBytes))
		}
		if executed.WindowChunks > 0 && executed.WindowChunks != report.Requested.WindowChunks {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf(
				"window chunks: requested %d, executed %d", report.Requested.WindowChunks, executed.WindowChunks))
		}
	} else if windowLoads == 0 && m.CacheHits == 0 {
		report.Mismatches = append(report.Mismatches, "window: the read recorded no window load; the read may not have reached the read planner")
	} else if windowLoads == 0 {
		report.Warnings = append(report.Warnings, "window: no read-window event was observed; the read may have been served from cache")
	}
	if requestedStreams > 1 {
		switch {
		case executed.Streams > 1:
			if executed.Streams > requestedStreams {
				report.Warnings = append(report.Warnings, fmt.Sprintf(
					"streams: the adaptive split used %d streams, above the requested %d (object-level throttle fallback)",
					executed.Streams, requestedStreams))
			}
		case executed.Streams == 1:
			report.Mismatches = append(report.Mismatches, fmt.Sprintf(
				"streams: requested %d parallel streams, executed 1 (parallel_range fan-out did not run)", requestedStreams))
		default:
			report.Warnings = append(report.Warnings, "streams: the read did not expose executed stream counts")
		}
	}
	if requestedPartBytes > 0 {
		switch {
		case executed.PartBytes <= 0:
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"part size: requested %d bytes but the read did not expose an executed part size", requestedPartBytes))
		case executed.PartBytes != requestedPartBytes:
			report.Mismatches = append(report.Mismatches, fmt.Sprintf(
				"part size: requested %d bytes, executed %d bytes", requestedPartBytes, executed.PartBytes))
		}
	}
	if prefetchDepth > 0 {
		switch {
		case executed.PrefetchWindows > 0:
		case report.Counters.PrefetchesDelta > 0:
		case report.Counters.WindowLoadsDelta > 0 && prefetchDepth == 1 && m.CacheHits > 0:
			report.Warnings = append(report.Warnings, "prefetch: the read hit cache; prefetch may not have been needed")
		default:
			report.Mismatches = append(report.Mismatches, fmt.Sprintf(
				"prefetch: requested depth %d but no prefetch window was recorded", prefetchDepth))
		}
	}
	if m.CacheHits > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"cache: %d of %d read calls hit cache, so the executed shape may not represent a cold read",
			m.CacheHits, m.ReadCalls))
	}
	if report.Counters.ReadTimeouts > 0 || report.Counters.PrefetchTimeouts > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"timeouts: read=%d prefetch=%d during the run; latency numbers include retries",
			report.Counters.ReadTimeouts, report.Counters.PrefetchTimeouts))
	}
	report.OK = len(report.Mismatches) == 0 && report.Error == ""
}

func finishVerifyRead(cmd *cobra.Command, rt cliruntime.Runtime, report *verifyReadReport, jsonOutput bool) error {
	if jsonOutput {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		printVerifyReadReport(cmd, report)
	}
	if len(report.Mismatches) > 0 {
		return rt.ExitError(cliruntime.ExitMismatch, fmt.Errorf("verify read: executed read shape does not match the requested policy (%d mismatch(es))", len(report.Mismatches)))
	}
	if report.Error != "" {
		return rt.ExitError(exitFailure, errors.New("verify read: "+report.Error))
	}
	return nil
}

func printVerifyReadReport(cmd *cobra.Command, report *verifyReadReport) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "verify read: mount %s path %s\n", report.Mount, report.Path)
	fmt.Fprintf(out, "  requested  window=%s (%d chunks) streams=%d prefetch_depth=%d part=%s\n",
		cliruntime.FormatBytes(report.Requested.WindowBytes), report.Requested.WindowChunks,
		report.Requested.Streams, report.Requested.PrefetchDepth, formatVerifyPart(report.Requested.PartBytes))
	fmt.Fprintf(out, "  executed   window=%s streams=%d part=%s prefetch_windows=%d\n",
		formatVerifyExecutedWindow(report), report.Executed.Streams,
		formatVerifyPart(report.Executed.PartBytes), report.Executed.PrefetchWindows)
	fmt.Fprintf(out, "  counters   prefetches=%+d window_loads=%+d interactive_chunks=%+d bulk_chunks=%+d\n",
		report.Counters.PrefetchesDelta, report.Counters.WindowLoadsDelta,
		report.Counters.InteractiveChunksDelta, report.Counters.BulkChunksDelta)
	if len(report.Measurements) > 0 {
		m := report.Measurements[0]
		fmt.Fprintf(out, "  measured   %s in %s, ttfb %s, %s, cache_hits=%d/%d\n",
			cliruntime.FormatBytes(m.Bytes), formatVerifyDuration(m.DurationMicros),
			formatVerifyDuration(m.TTFBMicros), formatVerifyRate(m.SteadyBPS), m.CacheHits, m.ReadCalls)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(out, "  warning: %s\n", warning)
	}
	for _, mismatch := range report.Mismatches {
		fmt.Fprintf(out, "  mismatch: %s\n", mismatch)
	}
	if report.Error != "" {
		fmt.Fprintf(out, "  error: %s\n", report.Error)
	}
	if report.OK {
		fmt.Fprintln(out, "  result: ok")
	} else {
		fmt.Fprintln(out, "  result: failed")
	}
}

func formatVerifyPart(part int64) string {
	if part <= 0 {
		return "auto"
	}
	return cliruntime.FormatBytes(part)
}

func formatVerifyExecutedWindow(report *verifyReadReport) string {
	if report.Executed.WindowBytes <= 0 {
		return "-"
	}
	return cliruntime.FormatBytes(report.Executed.WindowBytes)
}

func formatVerifyDuration(micros int64) string {
	return (time.Duration(micros) * time.Microsecond).Round(time.Millisecond).String()
}

func formatVerifyRate(bps int64) string {
	if bps <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f MiB/s", float64(bps)/(1024*1024))
}

func buildVerifyCounters(before, after *control.ReadMemoryMount) verifyReadCounters {
	var counters verifyReadCounters
	if before != nil {
		counters.PrefetchesBefore = int64(before.Runtime.Prefetches)
		counters.WindowLoadsBefore = int64(before.Runtime.WindowLoads)
	}
	if after != nil {
		counters.PrefetchesAfter = int64(after.Runtime.Prefetches)
		counters.WindowLoadsAfter = int64(after.Runtime.WindowLoads)
		counters.InteractiveChunksDelta = int64(after.Runtime.InteractiveWindowChunks) - runtimeChunkBase(before, func(r control.ReadMemoryMount) int64 {
			return int64(r.Runtime.InteractiveWindowChunks)
		})
		counters.BulkChunksDelta = int64(after.Runtime.BulkWindowChunks) - runtimeChunkBase(before, func(r control.ReadMemoryMount) int64 {
			return int64(r.Runtime.BulkWindowChunks)
		})
		counters.ReadTimeouts = int64(after.Runtime.ReadTimeouts)
		counters.PrefetchTimeouts = int64(after.Runtime.PrefetchTimeouts)
	}
	counters.PrefetchesDelta = counters.PrefetchesAfter - counters.PrefetchesBefore
	counters.WindowLoadsDelta = counters.WindowLoadsAfter - counters.WindowLoadsBefore
	return counters
}

func runtimeChunkBase(before *control.ReadMemoryMount, pick func(control.ReadMemoryMount) int64) int64 {
	if before == nil {
		return 0
	}
	return pick(*before)
}

func fetchReadMemoryMount(cmd *cobra.Command, mount string) (*control.ReadMemoryMount, error) {
	endpoint := "/v1/read-memory?limit=1&since=1m&mount=" + url.QueryEscape(mount)
	body, err := debugSocketGet(cmd.Context(), endpoint)
	if err != nil {
		return nil, err
	}
	var response control.ReadMemoryResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode read-memory: %w", err)
	}
	for i := range response.Mounts {
		if strings.EqualFold(response.Mounts[i].Mount, mount) {
			return &response.Mounts[i], nil
		}
	}
	return nil, nil
}

func parseDriverTestRuns(body []byte) ([]contracttest.TestRun, error) {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "{") {
		var single contracttest.TestRun
		if err := json.Unmarshal(body, &single); err != nil {
			return nil, err
		}
		return []contracttest.TestRun{single}, nil
	}
	var runs []contracttest.TestRun
	err := json.Unmarshal(body, &runs)
	return runs, err
}

func findReadRun(runs []contracttest.TestRun) *contracttest.TestRun {
	for i := range runs {
		if strings.EqualFold(runs[i].Spec, "read") || runs[i].Read != nil {
			return &runs[i]
		}
	}
	if len(runs) > 0 {
		return &runs[0]
	}
	return nil
}

func optionalSize(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	size, err := config.ParseSize(value)
	if err != nil {
		return 0, err
	}
	if size < 0 {
		return 0, fmt.Errorf("size must not be negative")
	}
	return size, nil
}

func parseVerifyOffset(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	offset := contracttest.ParseXferSize(value)
	if offset < 0 {
		return 0, fmt.Errorf("verify read: --offset must not be negative")
	}
	return offset, nil
}

func mustFlag(cmd *cobra.Command, name string) string {
	value, _ := cmd.Flags().GetString(name)
	return value
}
