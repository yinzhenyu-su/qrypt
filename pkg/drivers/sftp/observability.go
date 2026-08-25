package sftp

import (
	"context"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityWriter, drive.CapabilitySourceUploader, drive.CapabilityResumableUploader, drive.CapabilitySpace, drive.CapabilityPathResolver, drive.CapabilityRemoteNameResolver, drive.CapabilityMtime}
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{Driver: "sftp", Health: "ok", GeneratedAt: time.Now(), Stats: map[string]any{"address": d.address, drive.DebugStatRootPath: d.rootPath, "username": d.username}, Extra: map[string]any{drive.DebugExtraCredentialSource: "config"}}, nil
}

func (d *Driver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return drive.NormalizeMetricEvents("sftp", d.metrics.Events(since)), nil
}

func (d *Driver) recordOperation(ctx context.Context, operation, path string, started time.Time, bytes int64, err error) {
	if d.metrics == nil {
		return
	}
	finished := time.Now()
	d.metrics.Record(ctx, drive.MetricEvent{
		At:         finished,
		Layer:      "driver.sftp",
		Operation:  operation,
		Path:       path,
		Bytes:      bytes,
		Duration:   finished.Sub(started).String(),
		StartedAt:  started,
		FinishedAt: finished,
		Throughput: metricThroughput(bytes, finished.Sub(started)),
		Error:      metricError(err),
	})
}

func metricError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func metricThroughput(bytes int64, duration time.Duration) int64 {
	if bytes <= 0 || duration <= 0 {
		return 0
	}
	return int64(float64(bytes) / duration.Seconds())
}

type metricReadCloser struct {
	io.ReadCloser
	d         *Driver
	ctx       context.Context
	operation string
	path      string
	offset    int64
	requested int64
	started   time.Time
	bytes     int64
	recorded  bool
}

func (r *metricReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytes += int64(n)
	if err != nil {
		if err == io.EOF {
			r.record(nil)
			return n, io.EOF
		}
		r.record(err)
	}
	return n, err
}

func (r *metricReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.record(err)
	return err
}

func (r *metricReadCloser) record(err error) {
	if r.recorded {
		return
	}
	r.recorded = true
	r.d.recordOperation(r.ctx, r.operation, r.path, r.started, r.bytes, err)
}
