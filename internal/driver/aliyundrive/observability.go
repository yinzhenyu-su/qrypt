package aliyundrive

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
	}
}

func (d *Driver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	metrics, err := d.metricEvents(ctx, since)
	if err != nil {
		return nil, err
	}
	return drive.NormalizeMetricEvents("aliyundrive", metrics), nil
}
