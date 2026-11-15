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

// ReadProfile keeps Quark bulk reads single-stream by default: parallel ranges
// did not beat one stream in real-mount measurements, and four concurrent
// bodies trip the slow-read watchdog's per-stream floor. A mount may still opt
// into parallel reads with [mounts.read_profile] streams = N. The idle bound
// stays at the shared default; only the total budget is tightened.
func (d *Driver) ReadProfile() drive.ReadProfile {
	return drive.ReadProfile{
		BulkStreams:       1,
		FetchTotalTimeout: downloadTotalTimeout,
	}
}

func (d *Driver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	metrics, err := d.metricEvents(ctx, since)
	if err != nil {
		return nil, err
	}
	return drive.NormalizeMetricEvents("quark", metrics), nil
}
