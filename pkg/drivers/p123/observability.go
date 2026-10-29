package p123

import (
	"context"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Capabilities reports the optional behavior this driver implements. 123pan
// has no endpoint that sets mtime, so that capability stays undeclared.
func (d *Driver) Capabilities() []drive.Capability {
	return []drive.Capability{
		drive.CapabilityWriter,
		drive.CapabilitySourceUploader,
		drive.CapabilityResumableUploader,
		drive.CapabilityServerSideCopy,
		drive.CapabilitySpace,
		drive.CapabilityPathResolver,
	}
}

// RemoteHash reports the stored file's content MD5. 123pan records each
// object's content MD5 as its Etag, so sync and check can compare against a
// local hash without downloading the file.
func (d *Driver) RemoteHash(_ context.Context, entry drive.Entry) (drive.HashAlgorithm, string, error) {
	if entry.IsDir {
		return "", "", drive.ErrUnsupported
	}
	if meta, ok := metaFromEntry(entry); ok && meta.Etag != "" {
		return drive.HashMD5, strings.ToLower(meta.Etag), nil
	}
	return "", "", drive.ErrUnsupported
}

// RemoteHashInfo reports the provider-computed MD5 from the listing Etag.
func (d *Driver) RemoteHashInfo(ctx context.Context, entry drive.Entry) (drive.RemoteHashInfo, error) {
	algorithm, hash, err := d.RemoteHash(ctx, entry)
	if err != nil {
		return drive.RemoteHashInfo{}, err
	}
	return drive.RemoteHashInfo{Algorithm: algorithm, Hash: hash, Verified: true, Source: drive.RemoteHashSourceProvider}, nil
}

func (d *Driver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	metrics, err := d.metricEvents(ctx, since)
	if err != nil {
		return nil, err
	}
	return drive.NormalizeMetricEvents("123", metrics), nil
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

var _ drive.BandwidthLimitInstaller = (*Driver)(nil)
