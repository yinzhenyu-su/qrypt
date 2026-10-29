package drime

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) Capabilities() []drive.Capability {
	return []drive.Capability{
		drive.CapabilityMtime,
		drive.CapabilityPathResolver,
		drive.CapabilityRemoteNameResolver,
		drive.CapabilityResumableUploader,
		drive.CapabilityServerSideCopy,
		drive.CapabilityShare,
		drive.CapabilityShareFetch,
		drive.CapabilityShareReceive,
		drive.CapabilityShareSave,
		drive.CapabilitySourceUploader,
		drive.CapabilitySpace,
		drive.CapabilityWriter,
	}
}

func (d *Driver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	health := drive.HealthLevelOK
	lastErr := d.getLastError()
	if lastErr != "" {
		health = drive.HealthLevelDegraded
	}
	return drive.DebugSnapshot{
		Driver:      "drime",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			drive.DebugStatRootID:   d.rootID,
			drive.DebugStatRootPath: d.rootPath,
			"workspace_id":          d.workspaceID,
			"chunk_size":            d.chunkSize,
			"hard_delete":           d.hardDelete,
		},
		Extra: map[string]any{
			drive.DebugExtraInstantUploadCount: d.instantUploads.Load(),
			drive.DebugExtraLastError:          lastErr,
		},
	}, nil
}

func (d *Driver) Metrics(_ context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return drive.NormalizeMetricEvents("drime", d.cl.metricsSince(since)), nil
}

var (
	_ drive.ShareManager          = (*Driver)(nil)
	_ drive.ShareReceiver         = (*Driver)(nil)
	_ drive.ShareFetcher          = (*Driver)(nil)
	_ drive.ShareSaver            = (*Driver)(nil)
	_ drive.ProvenanceHasher      = (*Driver)(nil)
	_ drive.ProviderSpaceReporter = (*Driver)(nil)
	_ drive.EntryResolver         = (*Driver)(nil)
	_ drive.RemoteNameCodec       = (*Driver)(nil)
)
