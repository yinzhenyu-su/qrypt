package quark

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) Capabilities() []drive.Capability {
	return []drive.Capability{
		drive.CapabilityWriter,
		drive.CapabilitySourceUploader,
		drive.CapabilityResumableUploader,
		drive.CapabilitySpace,
		drive.CapabilityPathResolver,
		drive.CapabilityServerSideCopy,
		drive.CapabilityOfflineDownload,
		drive.CapabilityOfflineInspect,
		drive.CapabilityOfflineSelection,
		drive.CapabilityAcceleratedFetch,
		drive.CapabilityShare,
		drive.CapabilityShareReceive,
		drive.CapabilityShareSave,
		drive.CapabilityShareFetch,
		// Quark honors Range requests with 206, so the read domain owns
		// sub-range splitting and its fetch idle/total bounds.
		drive.CapabilityParallelRange,
	}
}

// downloadTotalTimeout bounds one Quark Range fetch. The CDN can degrade a
// single connection to a trickle for minutes, which the idle bound never sees
// because bytes keep arriving. A shorter total budget abandons that connection
// while the read layer still has room to retry, instead of showing a five
// minute stall on the mounted file.
const downloadTotalTimeout = 90 * time.Second

// ReadPolicy follows the released reader's 4 MiB bulk windows and adaptive
// lookahead, plus a 2 MiB sequential playback window. Playback arrives as small
// FUSE reads, which the caller window turns into single 512 KiB windows: one
// connection in flight sustains roughly 2.2 MB/s. A fixed 2 MiB sequential
// window with the adaptive four-window lookahead reaches roughly 4.5-5.7 MB/s
// for the same playback-shaped reads, while a 4 MiB window was slower in the
// same measurements (larger windows only widen the single-stream burst).
// Initial and seek access keep the caller window so playback start and seeks
// still see the first bytes after about 0.7 s. The shared budget allows four
// lookaheads plus three foreground workers; lookahead must not consume the
// foreground's entire allowance.
func (d *Driver) ReadPolicy() drive.ReadPolicy {
	p := drive.DefaultReadPolicy()
	p.Interactive.Sequential = drive.AccessPolicy{
		Window:   drive.WindowPolicy{Mode: drive.WindowFixed, Bytes: 2 << 20},
		Prefetch: drive.PrefetchPolicy{Mode: drive.PrefetchAdaptive},
	}
	p.Bulk.WindowBytes = 4 << 20
	p.Bulk.Streams = 3
	p.Bulk.Split = drive.SplitPolicy{PartBytes: 8 << 20, MinParallelBytes: 4 << 20}
	p.MaxInFlight = 7
	p.Fetch.Total = downloadTotalTimeout
	return p
}

func (d *Driver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	metrics, err := d.metricEvents(ctx, since)
	if err != nil {
		return nil, err
	}
	return drive.NormalizeMetricEvents("quark", metrics), nil
}
