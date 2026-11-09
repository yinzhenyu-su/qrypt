package koofr

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Capabilities declares exactly the implemented optional behavior: writer,
// source_uploader, space, path_resolver, remote_name_resolver, mtime
// (upload/copy-time only), server_side_copy, share, share_receive,
// share_fetch. Resumable uploads (single-request provider API), share save,
// and offline capabilities are absent.
func (d *Driver) Capabilities() []drive.Capability {
	return []drive.Capability{
		drive.CapabilityMtime,
		drive.CapabilityPathResolver,
		drive.CapabilityRemoteNameResolver,
		drive.CapabilityServerSideCopy,
		drive.CapabilityShare,
		drive.CapabilityShareFetch,
		drive.CapabilityShareReceive,
		drive.CapabilitySourceUploader,
		drive.CapabilitySpace,
		drive.CapabilityWriter,
		drive.CapabilityParallelRange,
	}
}

func (d *Driver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	health := drive.HealthLevelOK
	lastErr := d.getLastError()
	if lastErr != "" {
		health = drive.HealthLevelDegraded
	}
	extra := map[string]any{
		drive.DebugExtraLastError: lastErr,
		"upload_count":            d.uploads.Load(),
	}
	if requestID := d.cl.lastReqID(); requestID != "" {
		extra["last_request_id"] = requestID
	}
	stats := map[string]any{
		drive.DebugStatRootID:   d.rootPath,
		drive.DebugStatRootPath: d.rootPath,
		"mount_id":              d.mountID,
		"mount_name":            d.mountName,
		"api_base_url":          sanitizeURL(d.cl.base),
	}
	if d.virtualRoot() {
		stats["virtual_root"] = true
		stats["external_mounts"] = len(d.extMounts)
	}
	return drive.DebugSnapshot{
		Driver:      "koofr",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats:       stats,
		Extra:       extra,
	}, nil
}

func (d *Driver) Metrics(_ context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return drive.NormalizeMetricEvents("koofr", d.cl.metricsSince(since)), nil
}

var (
	_ drive.ShareManager     = (*Driver)(nil)
	_ drive.ShareLister      = (*Driver)(nil)
	_ drive.ShareReceiver    = (*Driver)(nil)
	_ drive.ShareFetcher     = (*Driver)(nil)
	_ drive.ShareLinkParser  = (*Driver)(nil)
	_ drive.RemoteHasher     = (*Driver)(nil)
	_ drive.ProvenanceHasher = (*Driver)(nil)
	_ drive.ServerSideCopier = (*Driver)(nil)
)
