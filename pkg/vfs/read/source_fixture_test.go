package read

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"io"
)

type testRangeHost interface {
	DriverRead(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error)
}
type testReadBackend interface {
	readWindowBackend
	Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error)
}
type testReaderDeps struct {
	Host       Host
	State      *State
	Observer   ReadObserver
	Health     HealthRecorder
	Counters   CounterRecorder
	Profile    drive.ReadPolicy
	Source     *drive.ReadSource
	ReadFaults ReadFaultInjector
}

func newTestReader(d testReaderDeps) *Reader {
	if d.Source == nil {
		if d.Profile.MaxInFlight == 0 {
			d.Profile = drive.DefaultReadPolicy()
		}
		var err error
		d.Source, err = drive.NewReadSource(hostReadDriver{host: d.Host, policy: d.Profile}, nil)
		if err != nil {
			panic(err)
		}
	}
	return NewReader(ReaderDeps{Host: d.Host, State: d.State, Observer: d.Observer, Health: d.Health, Counters: d.Counters, Source: d.Source, ReadFaults: d.ReadFaults})
}

// hostReadDriver adapts isolated read-domain hosts. Mounted readers receive the
// already-owned source instead, preserving driver capabilities and identity.
type hostReadDriver struct {
	drive.Driver
	host   Host
	policy drive.ReadPolicy
}

func (h hostReadDriver) Read(ctx context.Context, e drive.Entry, o, n int64) (io.ReadCloser, error) {
	return h.host.(testRangeHost).DriverRead(ctx, e, o, n)
}
func (h hostReadDriver) ReadPolicy() drive.ReadPolicy { return h.policy }
func (h hostReadDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityParallelRange}
}
