package localfs

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) Capabilities() []drive.Capability {
	return []drive.Capability{
		drive.CapabilityWriter,
		drive.CapabilitySourceUploader,
		drive.CapabilitySpace,
		drive.CapabilityPathResolver,
		drive.CapabilityRemoteNameResolver,
	}
}

func (d *Driver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	trace, err := d.DebugTrace(ctx, since)
	if err != nil {
		return nil, err
	}
	return drive.MetricsFromTrace("localfs", trace), nil
}
