package debug

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/contracttest"
)

// NewABCmd groups interleaved A/B comparisons. Each round runs both sides in
// alternating order so provider drift hits both shapes roughly equally, which
// is the part a hand-written batch of `probe test read` calls gets wrong.
func NewABCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ab",
		Short: "Compare two measurement configurations",
		Long: `Compare two measurement configurations with interleaved rounds.

The sides alternate A/B then B/A on successive rounds, so a drifting provider
or a warming CDN affects both sides instead of biasing whichever ran second.
The report includes the resolved read plan for each side and a median ratio;
use it instead of a batch of manual probe runs when deciding a read policy.`,
		Example: `  qrypt probe ab read --mount cloud --mount-point /mnt/qrypt/cloud --path /movie.mp4 --a control --b "window=4MiB"
  qrypt probe ab read --mount cloud --mount-point /mnt/qrypt/cloud --path /movie.mp4 \
    --a "window=2MiB,prefetch-depth=2" --b "window=4MiB,prefetch-depth=4" --rounds 3 --json`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	cmd.AddCommand(withProbeSocketFlag(rt, newABReadCmd(rt)))
	return cmd
}

func newABReadCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "read",
		Short: "Interleaved A/B comparison of two mounted read shapes",
		Long: `Measure the same file with two read shapes and report per-round and median
throughput. A shape is a comma separated list of measurement overrides:

  window=2MiB,prefetch-window=2MiB,prefetch-depth=4,part-size=auto,min-parallel=8MiB,streams=2

Use "control" (or an empty value) for the driver defaults. Every round clears
the read cache first, so the comparison is cold-start to cold-start.`,
		Example: `  qrypt probe ab read --mount cloud --mount-point /mnt/qrypt/cloud \
    --path /movie.mp4 --size 16m --a control --b "window=4MiB" --rounds 3`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE:              runABRead,
	}
	cmd.Flags().String("mount", "", "mount name for the measurements")
	cmd.Flags().String("mount-point", "", "actual FUSE mount directory")
	cmd.Flags().String("path", "", "existing file to read (mount-relative virtual path)")
	cmd.Flags().String("size", "16m", "bytes to read per round")
	cmd.Flags().String("block-size", "1m", "read block size in bytes, or k/m/g suffix")
	cmd.Flags().String("offset", "", "base read offset (default 0)")
	cmd.Flags().Bool("fresh-offsets", false, "advance the offset on every round so each round starts at new data")
	cmd.Flags().String("a", "control", `A-side shape, e.g. "window=2MiB" or control`)
	cmd.Flags().String("b", "control", `B-side shape, e.g. "window=4MiB" or control`)
	cmd.Flags().Int("rounds", 3, "interleaved rounds (1-10)")
	cmd.Flags().Bool("json", false, "print the report as JSON")
	return cmd
}

// readShapeSpec is one side of `probe ab read`: a set of measurement overrides
// applied to the read plan. The zero value means the driver/profile defaults,
// which makes a control side explicit instead of implicit.
type readShapeSpec struct {
	WindowBytes         int64
	PrefetchWindowBytes int64
	PrefetchDepth       *int
	PrefetchDisabled    bool
	PartBytes           *int64
	MinParallelBytes    *int64
	Streams             int
}

func parseReadShapeSpec(value string) (readShapeSpec, error) {
	spec := readShapeSpec{}
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "control") {
		return spec, nil
	}
	for _, field := range strings.Split(value, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		key, raw, ok := strings.Cut(field, "=")
		if !ok {
			key, raw = field, "true"
		}
		key = strings.ToLower(strings.TrimSpace(key))
		raw = strings.TrimSpace(raw)
		switch key {
		case "window":
			n, err := parseShapeSize(raw, 32<<20)
			if err != nil {
				return spec, fmt.Errorf("read shape %q: window: %w", value, err)
			}
			spec.WindowBytes = n
		case "prefetch-window":
			n, err := parseShapeSize(raw, 32<<20)
			if err != nil {
				return spec, fmt.Errorf("read shape %q: prefetch-window: %w", value, err)
			}
			spec.PrefetchWindowBytes = n
		case "prefetch-depth":
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 4 {
				return spec, fmt.Errorf("read shape %q: prefetch-depth must be between 1 and 4", value)
			}
			spec.PrefetchDepth = &n
			spec.PrefetchDisabled = false
		case "no-prefetch":
			disabled := raw == "" || strings.EqualFold(raw, "true")
			if !disabled && !strings.EqualFold(raw, "false") {
				return spec, fmt.Errorf("read shape %q: no-prefetch must be true or false", value)
			}
			spec.PrefetchDisabled = disabled
		case "part-size":
			if strings.EqualFold(raw, "auto") {
				var zero int64
				spec.PartBytes = &zero
				continue
			}
			n, err := parseShapeSize(raw, 32<<20)
			if err != nil {
				return spec, fmt.Errorf("read shape %q: part-size: %w", value, err)
			}
			spec.PartBytes = &n
		case "min-parallel":
			n, err := parseShapeSize(raw, 32<<20)
			if err != nil {
				return spec, fmt.Errorf("read shape %q: min-parallel: %w", value, err)
			}
			spec.MinParallelBytes = &n
		case "streams":
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || n > 32 {
				return spec, fmt.Errorf("read shape %q: streams must be between 0 and 32", value)
			}
			spec.Streams = n
		default:
			return spec, fmt.Errorf("read shape %q: unknown field %q (window, prefetch-window, prefetch-depth, no-prefetch, part-size, min-parallel, streams)", value, key)
		}
	}
	return spec, nil
}

func parseShapeSize(raw string, maxBytes int64) (int64, error) {
	n, err := config.ParseSize(raw)
	if err != nil || n <= 0 || n > maxBytes {
		return 0, fmt.Errorf("must be between 1 byte and %d bytes", maxBytes)
	}
	return n, nil
}

// equivalent reports whether two specs request the same overrides. Pointer
// fields hold the same value, so compare values rather than identity.
func (s readShapeSpec) equivalent(other readShapeSpec) bool {
	return s.WindowBytes == other.WindowBytes &&
		s.PrefetchWindowBytes == other.PrefetchWindowBytes &&
		s.Streams == other.Streams &&
		s.PrefetchDisabled == other.PrefetchDisabled &&
		equalInt(s.PrefetchDepth, other.PrefetchDepth) &&
		equalInt64(s.PartBytes, other.PartBytes) &&
		equalInt64(s.MinParallelBytes, other.MinParallelBytes)
}

func equalInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (s readShapeSpec) apply(req *contracttest.DriverTestRequest) {
	req.WindowBytes = s.WindowBytes
	req.PrefetchWindowBytes = s.PrefetchWindowBytes
	req.Streams = s.Streams
	if s.PrefetchDepth != nil {
		depth := *s.PrefetchDepth
		req.PrefetchDepth = &depth
	}
	if s.PrefetchDisabled {
		zero := 0
		req.PrefetchDepth = &zero
	}
	if s.PartBytes != nil {
		part := *s.PartBytes
		req.PartBytes = &part
	}
	if s.MinParallelBytes != nil {
		minParallel := *s.MinParallelBytes
		req.MinParallelBytes = &minParallel
	}
}

func (s readShapeSpec) String() string {
	if s.WindowBytes == 0 && s.PrefetchWindowBytes == 0 && s.PrefetchDepth == nil && !s.PrefetchDisabled &&
		s.PartBytes == nil && s.MinParallelBytes == nil && s.Streams == 0 {
		return "control"
	}
	var parts []string
	if s.WindowBytes > 0 {
		parts = append(parts, fmt.Sprintf("window=%d", s.WindowBytes))
	}
	if s.PrefetchWindowBytes > 0 {
		parts = append(parts, fmt.Sprintf("prefetch-window=%d", s.PrefetchWindowBytes))
	}
	if s.PrefetchDepth != nil {
		parts = append(parts, fmt.Sprintf("prefetch-depth=%d", *s.PrefetchDepth))
	}
	if s.PrefetchDisabled {
		parts = append(parts, "no-prefetch=true")
	}
	if s.PartBytes != nil {
		if *s.PartBytes == 0 {
			parts = append(parts, "part-size=auto")
		} else {
			parts = append(parts, fmt.Sprintf("part-size=%d", *s.PartBytes))
		}
	}
	if s.MinParallelBytes != nil {
		parts = append(parts, fmt.Sprintf("min-parallel=%d", *s.MinParallelBytes))
	}
	if s.Streams > 0 {
		parts = append(parts, fmt.Sprintf("streams=%d", s.Streams))
	}
	return strings.Join(parts, ",")
}

type abReadSample struct {
	Round   int                           `json:"round"`
	Order   string                        `json:"order"`
	AOffset int64                         `json:"a_offset_bytes"`
	BOffset int64                         `json:"b_offset_bytes"`
	ABPS    int64                         `json:"a_end_to_end_bps"`
	BBPS    int64                         `json:"b_end_to_end_bps"`
	ATTFB   int64                         `json:"a_ttfb_us,omitempty"`
	BTTFB   int64                         `json:"b_ttfb_us,omitempty"`
	APlan   *contracttest.MountedReadPlan `json:"a_resolved_plan,omitempty"`
	BPlan   *contracttest.MountedReadPlan `json:"b_resolved_plan,omitempty"`
}

type abReadSummary struct {
	AMedianBPS int64   `json:"a_median_bps"`
	BMedianBPS int64   `json:"b_median_bps"`
	Ratio      float64 `json:"ratio"`
	Winner     string  `json:"winner"`
	DeltaPct   float64 `json:"delta_pct"`
}

type abReadReport struct {
	SchemaVersion int            `json:"schema_version"`
	Kind          string         `json:"kind"`
	Mount         string         `json:"mount"`
	Path          string         `json:"path"`
	SizeBytes     int64          `json:"size_bytes"`
	Rounds        int            `json:"rounds"`
	AShape        string         `json:"a_shape"`
	BShape        string         `json:"b_shape"`
	Samples       []abReadSample `json:"samples"`
	Summary       abReadSummary  `json:"summary"`
}

func runABRead(cmd *cobra.Command, _ []string) error {
	mount := mustFlag(cmd, "mount")
	if mount == "" {
		return fmt.Errorf("ab read requires --mount")
	}
	mountPoint := mustFlag(cmd, "mount-point")
	if mountPoint == "" {
		return fmt.Errorf("ab read requires --mount-point")
	}
	path := mustFlag(cmd, "path")
	if path == "" {
		return fmt.Errorf("ab read requires --path (it measures an existing file instead of writing a fixture)")
	}
	size := contracttest.ParseXferSize(mustFlag(cmd, "size"))
	if size <= 0 || size > contracttest.MaxMountedReadTestSize {
		return fmt.Errorf("ab read --size must be between 1 and %d bytes", contracttest.MaxMountedReadTestSize)
	}
	blockSize := contracttest.ParseXferSize(mustFlag(cmd, "block-size"))
	if blockSize <= 0 || blockSize > 16<<20 {
		return fmt.Errorf("ab read --block-size must be between 1 byte and 16 MiB")
	}
	rounds, _ := cmd.Flags().GetInt("rounds")
	if rounds < 1 || rounds > 10 {
		return fmt.Errorf("ab read --rounds must be between 1 and 10")
	}
	freshOffsets, _ := cmd.Flags().GetBool("fresh-offsets")
	baseOffset := contracttest.ParseXferSize(mustFlag(cmd, "offset"))
	if baseOffset < 0 {
		return fmt.Errorf("ab read --offset must not be negative")
	}
	aSpec, err := parseReadShapeSpec(mustFlag(cmd, "a"))
	if err != nil {
		return err
	}
	bSpec, err := parseReadShapeSpec(mustFlag(cmd, "b"))
	if err != nil {
		return err
	}
	if aSpec.equivalent(bSpec) {
		return fmt.Errorf("ab read: --a and --b describe the same shape; nothing to compare")
	}

	sizeText := strconv.FormatInt(size, 10)
	blockText := strconv.FormatInt(blockSize, 10)
	base := contracttest.DriverTestRequest{
		Test:        "read",
		Mount:       contracttest.NormalizeMountName(mount),
		MountPoint:  mountPoint,
		Path:        path,
		Length:      sizeText,
		BlockSize:   blockText,
		CacheMode:   "cold",
		ReadPattern: "sequential",
		Samples:     1,
		HoldOpen:    true,
	}

	report := abReadReport{
		SchemaVersion: 1,
		Kind:          "ab_read",
		Mount:         base.Mount,
		Path:          path,
		SizeBytes:     size,
		Rounds:        rounds,
		AShape:        aSpec.String(),
		BShape:        bSpec.String(),
	}
	aRates := make([]int64, 0, rounds)
	bRates := make([]int64, 0, rounds)
	for round := 0; round < rounds; round++ {
		offset := baseOffset
		if freshOffsets {
			offset = baseOffset + int64(round)*size
		}
		type side struct {
			name string
			spec readShapeSpec
		}
		sides := []side{{name: "a", spec: aSpec}, {name: "b", spec: bSpec}}
		if round%2 == 1 {
			sides[0], sides[1] = sides[1], sides[0]
		}
		order := sides[0].name + "," + sides[1].name
		sample := abReadSample{Round: round + 1, Order: order, AOffset: offset, BOffset: offset}
		for _, s := range sides {
			req := base
			s.spec.apply(&req)
			req.Offset = strconv.FormatInt(offset, 10)
			bps, ttfb, plan, err := measureABRead(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("ab read round %d side %s: %w", round+1, s.name, err)
			}
			if s.name == "a" {
				sample.ABPS, sample.ATTFB, sample.APlan = bps, ttfb, plan
				aRates = append(aRates, bps)
			} else {
				sample.BBPS, sample.BTTFB, sample.BPlan = bps, ttfb, plan
				bRates = append(bRates, bps)
			}
		}
		report.Samples = append(report.Samples, sample)
	}
	report.Summary = summarizeABRead(aRates, bRates)

	if jsonOutput, _ := cmd.Flags().GetBool("json"); jsonOutput {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "ab read: %s %s, %d MiB per round, %d round(s)\n", report.Mount, report.Path, size>>20, rounds)
	fmt.Fprintf(out, "  A: %s\n  B: %s\n", report.AShape, report.BShape)
	for _, sample := range report.Samples {
		fmt.Fprintf(out, "  round %d (%s): A %s  B %s\n", sample.Round, sample.Order, formatBPS(sample.ABPS), formatBPS(sample.BBPS))
	}
	fmt.Fprintf(out, "  median: A %s  B %s  ratio %.2f  winner %s (%+.1f%%)\n",
		formatBPS(report.Summary.AMedianBPS), formatBPS(report.Summary.BMedianBPS),
		report.Summary.Ratio, strings.ToUpper(report.Summary.Winner), report.Summary.DeltaPct)
	if plan := report.Samples[0].APlan; plan != nil {
		fmt.Fprintf(out, "  A resolved: %s %s window=%d streams=%d part=%d prefetch=%dx%d\n",
			plan.Stage, plan.Intent, plan.WindowBytes, plan.MaxStreams, plan.PartBytes, plan.PrefetchDepth, plan.PrefetchWindowBytes)
	}
	if plan := report.Samples[0].BPlan; plan != nil {
		fmt.Fprintf(out, "  B resolved: %s %s window=%d streams=%d part=%d prefetch=%dx%d\n",
			plan.Stage, plan.Intent, plan.WindowBytes, plan.MaxStreams, plan.PartBytes, plan.PrefetchDepth, plan.PrefetchWindowBytes)
	}
	return nil
}

func measureABRead(ctx context.Context, req contracttest.DriverTestRequest) (int64, int64, *contracttest.MountedReadPlan, error) {
	if err := ValidateDriverTestRequest(req); err != nil {
		return 0, 0, nil, err
	}
	body, err := debugSocketPostJSON(ctx, "/v1/driver/test", req)
	if err != nil {
		if detail, routeMissing := debugSocket404Detail(err); detail != "" {
			return 0, 0, nil, fmt.Errorf("%s", detail)
		} else if routeMissing {
			return 0, 0, nil, fmt.Errorf("probe test endpoint is not available on this socket; restart the qrypt mount process with the current binary")
		}
		return 0, 0, nil, err
	}
	runs, err := parseDriverTestRuns(body)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("decode measurement: %w", err)
	}
	run := findReadRun(runs)
	if run == nil || run.Read == nil || len(run.Read.Measurements) == 0 {
		if run != nil && run.Error != "" {
			return 0, 0, nil, fmt.Errorf("measurement failed: %s", run.Error)
		}
		return 0, 0, nil, fmt.Errorf("measurement returned no read samples")
	}
	measurement := run.Read.Measurements[len(run.Read.Measurements)-1]
	for i := len(run.Read.Measurements) - 1; i >= 0; i-- {
		if run.Read.Measurements[i].Mode == "cold" {
			measurement = run.Read.Measurements[i]
			break
		}
	}
	if !run.Pass && measurement.EndToEndBPS == 0 {
		return 0, 0, nil, fmt.Errorf("measurement failed: %s", runError(run))
	}
	return measurement.EndToEndBPS, measurement.TTFBMicros, run.Read.ResolvedPlan, nil
}

func runError(run *contracttest.TestRun) string {
	if run.Error != "" {
		return run.Error
	}
	return "read benchmark reported a failure"
}

func summarizeABRead(aRates, bRates []int64) abReadSummary {
	summary := abReadSummary{AMedianBPS: medianBPS(aRates), BMedianBPS: medianBPS(bRates)}
	if summary.BMedianBPS > 0 {
		summary.Ratio = float64(summary.AMedianBPS) / float64(summary.BMedianBPS)
	}
	switch {
	case summary.Ratio >= 1.05:
		summary.Winner = "a"
	case summary.Ratio > 0 && summary.Ratio <= 0.95:
		summary.Winner = "b"
	default:
		summary.Winner = "tie"
	}
	if summary.BMedianBPS > 0 {
		summary.DeltaPct = (summary.Ratio - 1) * 100
	}
	return summary
}

func medianBPS(rates []int64) int64 {
	if len(rates) == 0 {
		return 0
	}
	sorted := append([]int64(nil), rates...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

func formatBPS(bps int64) string {
	if bps < 0 {
		bps = 0
	}
	units := []string{"B/s", "KiB/s", "MiB/s", "GiB/s"}
	value := float64(bps)
	unit := units[0]
	for i := 0; i < len(units)-1 && value >= 1024; i++ {
		value /= 1024
		unit = units[i+1]
	}
	return fmt.Sprintf("%.2f %s", value, unit)
}
