package contracttest

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
	vfsread "github.com/yinzhenyu/qrypt/pkg/vfs/read"
)

// Read tuning runs the same playback-shaped read under a sequence of read
// shapes, then reports the fastest shape per phase. It is the automated form
// of the manual tuning sessions: interactive window sweep, lookahead depth at
// the best window, then a bulk download sweep.
const (
	tuneDefaultSize      = 32 << 20
	tuneDefaultRounds    = 3
	tuneMaxRounds        = 5
	tuneDefaultBlockSize = 16 << 10
)

// ReadTuneVariant describes one shape measured by a tune run.
type ReadTuneVariant struct {
	Name                string `json:"name"`
	Mode                string `json:"mode"`
	WindowBytes         int64  `json:"window_bytes,omitempty"`
	PrefetchWindowBytes int64  `json:"prefetch_window_bytes,omitempty"`
	PrefetchDepth       *int   `json:"prefetch_depth,omitempty"`
	Streams             int    `json:"streams,omitempty"`
	PartBytes           *int64 `json:"part_bytes,omitempty"`
	MinParallelBytes    *int64 `json:"min_parallel_bytes,omitempty"`
}

// ReadTuneSample is one measured read of one variant in one round.
type ReadTuneSample struct {
	Variant    string  `json:"variant"`
	Mode       string  `json:"mode"`
	Round      int     `json:"round"`
	Bytes      int64   `json:"bytes"`
	DurationMS int64   `json:"duration_ms"`
	BPS        float64 `json:"bps"`
	TTFBUS     int64   `json:"ttfb_us,omitempty"`
}

// ReadTuneSummary aggregates the rounds of one variant.
type ReadTuneSummary struct {
	ReadTuneVariant
	Samples   int     `json:"samples"`
	MedianBPS float64 `json:"median_bps"`
	MinBPS    float64 `json:"min_bps"`
	MaxBPS    float64 `json:"max_bps"`
}

// ReadTuneRecommendation is the winning shape plus the TOML that reproduces it.
type ReadTuneRecommendation struct {
	InteractiveWindowBytes         int64    `json:"interactive_window_bytes,omitempty"`
	InteractivePrefetchWindowBytes int64    `json:"interactive_prefetch_window_bytes,omitempty"`
	InteractivePrefetchDepth       *int     `json:"interactive_prefetch_depth,omitempty"`
	BulkWindowBytes                int64    `json:"bulk_window_bytes,omitempty"`
	BulkStreams                    int      `json:"bulk_streams,omitempty"`
	BulkPartBytes                  *int64   `json:"bulk_part_bytes,omitempty"`
	BulkMinParallelBytes           *int64   `json:"bulk_min_parallel_bytes,omitempty"`
	Evidence                       []string `json:"evidence,omitempty"`
	Config                         string   `json:"config,omitempty"`
}

// ReadTuneDetails carries the tune-specific payload of a TestRun.
type ReadTuneDetails struct {
	Path           string                 `json:"path"`
	MountPoint     string                 `json:"mount_point"`
	SizeBytes      int64                  `json:"size_bytes"`
	Rounds         int                    `json:"rounds"`
	Samples        []ReadTuneSample       `json:"samples,omitempty"`
	Summaries      []ReadTuneSummary      `json:"summaries,omitempty"`
	Recommendation ReadTuneRecommendation `json:"recommendation"`
}

// RunVFSReadTune measures interactive and bulk read shapes on one mounted
// file and recommends the fastest. It needs a readable existing file: tuning
// measures content that is already remote, so it never uploads a fixture.
func RunVFSReadTune(ctx context.Context, fs vfs.FileSystem, mount string, req DriverTestRequest) TestRun {
	run := TestRun{Spec: "tuneread", Mount: mount, Started: time.Now()}
	finish := func(err error) TestRun {
		run.Finished = time.Now()
		run.DurationMS = run.Finished.Sub(run.Started).Milliseconds()
		run.Duration = (time.Duration(run.DurationMS) * time.Millisecond).String()
		if err != nil {
			run.Error = err.Error()
			run.Pass = false
			return run
		}
		run.Pass = true
		return run
	}
	if strings.TrimSpace(req.Path) == "" {
		return finish(fmt.Errorf("tuneread requires --path pointing at an existing readable file"))
	}
	if strings.TrimSpace(req.MountPoint) == "" {
		return finish(fmt.Errorf("tuneread requires --mount-point pointing at the FUSE mount"))
	}
	overrider, ok := fs.(readPlanOverrider)
	if !ok {
		return finish(fmt.Errorf("tuneread requires plan-override support"))
	}
	mountedPath := mountPointJoin(req.MountPoint, req.Path)
	if _, err := os.Stat(mountedPath); err != nil {
		return finish(fmt.Errorf("stat tuned file: %w", err))
	}
	size := int64(tuneDefaultSize)
	if strings.TrimSpace(req.Size) != "" {
		size = ParseXferSize(req.Size)
	}
	if size <= 0 {
		return finish(fmt.Errorf("tuneread size must be a positive byte count"))
	}
	if size > 256<<20 {
		size = 256 << 20
	}
	rounds := req.Samples
	if rounds <= 0 {
		rounds = tuneDefaultRounds
	}
	if rounds > tuneMaxRounds {
		rounds = tuneMaxRounds
	}
	blockSize := int64(tuneDefaultBlockSize)
	if strings.TrimSpace(req.BlockSize) != "" {
		if parsed := ParseXferSize(req.BlockSize); parsed > 0 {
			blockSize = parsed
		}
	}

	defer overrider.ClearReadPlanOverride()
	clearMountedReadCache(fs, mount) //nolint:errcheck // best-effort: a shared cache only warms the sample

	details := &ReadTuneDetails{Path: req.Path, MountPoint: req.MountPoint, SizeBytes: size, Rounds: rounds}
	tuneStart := time.Now()
	measured := make([]ReadTuneVariant, 0, 16)
	runPhase := func(variants []ReadTuneVariant) error {
		measured = append(measured, variants...)
		for round := 1; round <= rounds; round++ {
			for _, variant := range rotateVariants(variants, round-1) {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := overrider.SetReadPlanOverride(mount, tuneOverride(variant)); err != nil {
					return fmt.Errorf("apply %s: %w", variant.Name, err)
				}
				// Every variant starts from a cold cache: a previous variant has
				// just read the same region, and a warm hit would measure memory
				// instead of the provider.
				clearMountedReadCache(fs, mount) //nolint:errcheck // a shared cache only warms the sample
				sample, err := measureTuneVariant(ctx, fs, mount, req, overrider, variant, mountedPath, size, blockSize, round)
				details.Samples = append(details.Samples, sample)
				step := TestStep{Operation: "tune", Name: fmt.Sprintf("%s round %d", variant.Name, round), OK: err == nil, Duration: (time.Duration(sample.DurationMS) * time.Millisecond).String(), DurationMS: sample.DurationMS}
				if err != nil {
					step.Error = err.Error()
				}
				run.Steps = append(run.Steps, step)
				if err != nil {
					return fmt.Errorf("measure %s: %w", variant.Name, err)
				}
			}
		}
		return nil
	}
	if err := runPhase(tuneWindowVariants()); err != nil {
		run.Tune = details
		return finish(err)
	}
	bestWindow := bestTuneWindow(summarizeTuneSamples(details.Samples, measured))
	if err := runPhase(tuneDepthVariants(bestWindow)); err != nil {
		run.Tune = details
		return finish(err)
	}
	if err := runPhase(tuneBulkVariants()); err != nil {
		run.Tune = details
		return finish(err)
	}
	overrider.ClearReadPlanOverride()
	details.Summaries = summarizeTuneSamples(details.Samples, measured)
	details.Recommendation = tuneRecommendation(details.Summaries)
	run.Steps = append(run.Steps, TestStep{Operation: "tune", Name: "recommend", OK: true, Duration: (time.Duration(time.Since(tuneStart).Milliseconds()) * time.Millisecond).String(), DurationMS: time.Since(tuneStart).Milliseconds()})
	run.Tune = details
	clearMountedReadCache(fs, mount) //nolint:errcheck // return the mount to its warm-cache behaviour
	return finish(nil)
}

func tuneWindowVariants() []ReadTuneVariant {
	windows := []int64{0, 1 << 20, 2 << 20, 4 << 20, 8 << 20}
	variants := make([]ReadTuneVariant, 0, len(windows))
	for _, window := range windows {
		variants = append(variants, ReadTuneVariant{Name: tuneWindowName(window), Mode: "interactive", WindowBytes: window, PrefetchWindowBytes: window})
	}
	return variants
}

// tuneDepthVariants measures lookahead depth at the winning read window, so
// the depth phase is not biased by a window the sweep just rejected.
func tuneDepthVariants(windowBytes int64) []ReadTuneVariant {
	variants := make([]ReadTuneVariant, 0, 4)
	variants = append(variants, ReadTuneVariant{Name: "depth=driver", Mode: "interactive", WindowBytes: windowBytes, PrefetchWindowBytes: windowBytes})
	for _, depth := range []int{2, 4, 0} {
		d := depth
		variants = append(variants, ReadTuneVariant{Name: fmt.Sprintf("depth=%d", depth), Mode: "interactive", WindowBytes: windowBytes, PrefetchWindowBytes: windowBytes, PrefetchDepth: &d})
	}
	return variants
}

func tuneBulkVariants() []ReadTuneVariant {
	variants := make([]ReadTuneVariant, 0, 4)
	for _, window := range []int64{8 << 20, 16 << 20} {
		for _, streams := range []int{1, 2} {
			variants = append(variants, ReadTuneVariant{
				Name:        fmt.Sprintf("bulk window=%s streams=%d", formatTuneBytes(window), streams),
				Mode:        "bulk",
				WindowBytes: window,
				Streams:     streams,
			})
		}
	}
	return variants
}

// bestTuneWindow returns the window whose median playback rate is highest,
// preferring the caller window when no measured window beats it.
func bestTuneWindow(summaries []ReadTuneSummary) int64 {
	best := int64(0)
	bestRate := -1.0
	for _, summary := range summaries {
		if summary.Mode != "interactive" || strings.HasPrefix(summary.Name, "depth=") {
			continue
		}
		if summary.MedianBPS > bestRate {
			bestRate = summary.MedianBPS
			best = summary.WindowBytes
		}
	}
	return best
}

func tuneWindowName(window int64) string {
	if window <= 0 {
		return "window=caller"
	}
	return "window=" + formatTuneBytes(window)
}

func formatTuneBytes(value int64) string {
	switch {
	case value >= 1<<20 && value%(1<<20) == 0:
		return fmt.Sprintf("%dMiB", value>>20)
	case value >= 1<<10 && value%(1<<10) == 0:
		return fmt.Sprintf("%dKiB", value>>10)
	default:
		return fmt.Sprintf("%dB", value)
	}
}

func tuneOverride(variant ReadTuneVariant) vfs.ReadPlanOverride {
	override := vfs.ReadPlanOverride{PrefetchDepth: variant.PrefetchDepth}
	if variant.WindowBytes > 0 {
		override.WindowChunks = int((variant.WindowBytes + vfsread.ChunkSize - 1) / vfsread.ChunkSize)
	}
	if variant.PrefetchWindowBytes > 0 {
		override.PrefetchChunks = int((variant.PrefetchWindowBytes + vfsread.ChunkSize - 1) / vfsread.ChunkSize)
	}
	if variant.Mode == "bulk" {
		override.MaxStreams = variant.Streams
		override.PartBytes = variant.PartBytes
		override.MinParallelBytes = variant.MinParallelBytes
	}
	return override
}

func rotateVariants(variants []ReadTuneVariant, by int) []ReadTuneVariant {
	if len(variants) == 0 {
		return nil
	}
	by %= len(variants)
	out := make([]ReadTuneVariant, 0, len(variants))
	out = append(out, variants[by:]...)
	out = append(out, variants[:by]...)
	return out
}

func measureTuneVariant(ctx context.Context, fs vfs.FileSystem, mount string, req DriverTestRequest, overrider readPlanOverrider, variant ReadTuneVariant, mountedPath string, size, blockSize int64, round int) (ReadTuneSample, error) {
	sample := ReadTuneSample{Variant: variant.Name, Mode: variant.Mode, Round: round, Bytes: size}
	if variant.Mode == "bulk" {
		return measureTuneBulk(ctx, fs, req.Path, variant, size, sample)
	}
	measurement, _, err := measureMountedFile(ctx, fs, mount, mountedPath, "", blockSize, 0, size, "cold", round, nil, false)
	sample.DurationMS = measurement.DurationMicros / 1000
	sample.TTFBUS = measurement.TTFBMicros
	if measurement.DurationMicros > 0 {
		sample.BPS = float64(measurement.Bytes) / (float64(measurement.DurationMicros) / 1e6)
	}
	return sample, err
}

func measureTuneBulk(ctx context.Context, fs vfs.FileSystem, path string, variant ReadTuneVariant, size int64, sample ReadTuneSample) (ReadTuneSample, error) {
	streamer, ok := fs.(interface {
		ReadStream(context.Context, string) (io.ReadCloser, error)
	})
	if !ok {
		return sample, fmt.Errorf("bulk tuning requires the VFS ReadStream surface")
	}
	started := time.Now()
	rc, err := streamer.ReadStream(vfsread.WithReadIntent(ctx, vfsread.ReadIntentBulk), path)
	if err != nil {
		return sample, err
	}
	defer rc.Close() //nolint:errcheck // closing a read stream cannot lose data
	written, err := io.CopyN(io.Discard, rc, size)
	elapsed := time.Since(started)
	sample.Bytes = written
	sample.DurationMS = elapsed.Milliseconds()
	if elapsed > 0 {
		sample.BPS = float64(written) / elapsed.Seconds()
	}
	return sample, err
}

func summarizeTuneSamples(samples []ReadTuneSample, variants []ReadTuneVariant) []ReadTuneSummary {
	descriptors := make(map[string]ReadTuneVariant, len(variants))
	order := make([]string, 0, len(variants))
	for _, variant := range variants {
		descriptors[variant.Name] = variant
		order = append(order, variant.Name)
	}
	byVariant := make(map[string][]ReadTuneSample, len(variants))
	for _, sample := range samples {
		byVariant[sample.Variant] = append(byVariant[sample.Variant], sample)
	}
	out := make([]ReadTuneSummary, 0, len(order))
	for _, name := range order {
		group := byVariant[name]
		if len(group) == 0 {
			continue
		}
		rates := make([]float64, 0, len(group))
		minRate, maxRate := group[0].BPS, group[0].BPS
		for _, sample := range group {
			rates = append(rates, sample.BPS)
			minRate = min(minRate, sample.BPS)
			maxRate = max(maxRate, sample.BPS)
		}
		sort.Float64s(rates)
		median := rates[len(rates)/2]
		if len(rates)%2 == 0 {
			median = (rates[len(rates)/2-1] + rates[len(rates)/2]) / 2
		}
		out = append(out, ReadTuneSummary{ReadTuneVariant: descriptors[name], Samples: len(group), MedianBPS: median, MinBPS: minRate, MaxBPS: maxRate})
	}
	return out
}

func tuneRecommendation(summaries []ReadTuneSummary) ReadTuneRecommendation {
	var rec ReadTuneRecommendation
	bestInteractive := -1.0
	bestDepth := -1.0
	bestDepthLabel := ""
	bestBulk := -1.0
	for _, summary := range summaries {
		switch summary.Mode {
		case "interactive":
			switch {
			case strings.HasPrefix(summary.Name, "depth="):
				if summary.MedianBPS > bestDepth {
					bestDepth = summary.MedianBPS
					if depth := parseTuneDepth(summary.Name); depth != nil {
						rec.InteractivePrefetchDepth = depth
						bestDepthLabel = summary.Name
					} else {
						rec.InteractivePrefetchDepth = nil
						bestDepthLabel = "driver default"
					}
				}
			default:
				if summary.MedianBPS > bestInteractive {
					bestInteractive = summary.MedianBPS
					rec.InteractiveWindowBytes = summary.WindowBytes
					rec.InteractivePrefetchWindowBytes = summary.PrefetchWindowBytes
				}
			}
		case "bulk":
			if summary.MedianBPS > bestBulk {
				bestBulk = summary.MedianBPS
				rec.BulkWindowBytes = summary.WindowBytes
				rec.BulkStreams = summary.Streams
				rec.BulkPartBytes = summary.PartBytes
				rec.BulkMinParallelBytes = summary.MinParallelBytes
			}
		}
	}
	if bestInteractive > 0 {
		rec.Evidence = append(rec.Evidence, fmt.Sprintf("interactive window %s: %.1f MB/s", formatTuneBytes(rec.InteractiveWindowBytes), bestInteractive/1e6))
	}
	switch {
	case rec.InteractivePrefetchDepth != nil && *rec.InteractivePrefetchDepth == 0:
		rec.Evidence = append(rec.Evidence, "lookahead disabled is fastest")
	case rec.InteractivePrefetchDepth != nil:
		rec.Evidence = append(rec.Evidence, fmt.Sprintf("lookahead %s: %.1f MB/s", bestDepthLabel, bestDepth/1e6))
	case bestDepthLabel != "":
		rec.Evidence = append(rec.Evidence, fmt.Sprintf("%s lookahead: %.1f MB/s", bestDepthLabel, bestDepth/1e6))
	}
	if bestBulk > 0 {
		rec.Evidence = append(rec.Evidence, fmt.Sprintf("bulk window %s streams %d: %.1f MB/s", formatTuneBytes(rec.BulkWindowBytes), rec.BulkStreams, bestBulk/1e6))
	}
	rec.Config = tuneConfigSnippet(rec)
	return rec
}

func parseTuneDepth(name string) *int {
	var depth int
	if _, err := fmt.Sscanf(name, "depth=%d", &depth); err != nil {
		return nil
	}
	return &depth
}

func tuneConfigSnippet(rec ReadTuneRecommendation) string {
	var b strings.Builder
	b.WriteString("[mounts.read.interactive]\n")
	if rec.InteractiveWindowBytes > 0 {
		fmt.Fprintf(&b, "window = %q\n", formatTuneBytes(rec.InteractiveWindowBytes))
	}
	if rec.InteractivePrefetchWindowBytes > 0 {
		fmt.Fprintf(&b, "prefetch_window = %q\n", formatTuneBytes(rec.InteractivePrefetchWindowBytes))
	}
	if rec.InteractivePrefetchDepth != nil {
		fmt.Fprintf(&b, "prefetch_depth = %d\n", *rec.InteractivePrefetchDepth)
	}
	if rec.BulkWindowBytes > 0 || rec.BulkStreams > 0 {
		b.WriteString("\n[mounts.read.bulk]\n")
		if rec.BulkWindowBytes > 0 {
			fmt.Fprintf(&b, "window = %q\n", formatTuneBytes(rec.BulkWindowBytes))
		}
		if rec.BulkStreams > 0 {
			fmt.Fprintf(&b, "streams = %d\n", rec.BulkStreams)
		}
		if rec.BulkPartBytes != nil && *rec.BulkPartBytes > 0 {
			fmt.Fprintf(&b, "part_size = %q\n", formatTuneBytes(*rec.BulkPartBytes))
		}
		if rec.BulkMinParallelBytes != nil && *rec.BulkMinParallelBytes > 0 {
			fmt.Fprintf(&b, "min_parallel_size = %q\n", formatTuneBytes(*rec.BulkMinParallelBytes))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func mountPointJoin(mountPoint, virtualPath string) string {
	return mountPoint + "/" + strings.TrimPrefix(virtualPath, "/")
}
