package read

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

func TestReadIntentRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := ReadIntentFromContext(ctx); got != ReadIntentUnspecified {
		t.Fatalf("missing intent = %v, want unspecified", got)
	}
	for _, intent := range []ReadIntent{ReadIntentInteractive, ReadIntentBulk, ReadIntentProbe} {
		if got := ReadIntentFromContext(WithReadIntent(ctx, intent)); got != intent {
			t.Fatalf("intent round trip = %v, want %v", got, intent)
		}
	}
	if ReadIntentBulk.String() != "bulk" || ReadIntentProbe.String() != "probe" || ReadIntentInteractive.String() != "interactive" {
		t.Fatalf("unexpected intent strings: %q %q %q", ReadIntentBulk, ReadIntentProbe, ReadIntentInteractive)
	}
}

func TestPlanForUnspecifiedKeepsExistingBehavior(t *testing.T) {
	plan := planFor(ReadIntentUnspecified, drive.DefaultReadProfile(), 64<<20)
	if plan.streams != 1 || plan.windowChunks != 0 || plan.prefetchDepth != -1 || !plan.cacheWrites {
		t.Fatalf("unspecified plan = %+v, want existing-behavior sentinels", plan)
	}
}

func TestPlanForBulkUsesProfileAndThreshold(t *testing.T) {
	profile := drive.DefaultReadProfile()
	profile.BulkStreams = 4
	profile.ParallelThreshold = 8 << 20

	large := planFor(ReadIntentBulk, profile, 16<<20)
	if large.streams != 4 || large.windowChunks != profile.BulkWindowChunks || large.prefetchDepth != profile.PrefetchDepth || large.cacheWrites {
		t.Fatalf("large bulk plan = %+v", large)
	}

	small := planFor(ReadIntentBulk, profile, 1<<20)
	if small.streams != 1 {
		t.Fatalf("small bulk streams = %d, want 1", small.streams)
	}
}

func TestPlanForInteractiveAndProbe(t *testing.T) {
	profile := drive.DefaultReadProfile()

	interactive := planFor(ReadIntentInteractive, profile, 16<<20)
	if interactive.streams != 1 || interactive.windowChunks != profile.InteractiveWindowChunks || !interactive.cacheWrites {
		t.Fatalf("interactive plan = %+v", interactive)
	}

	probe := planFor(ReadIntentProbe, profile, 16<<20)
	if probe.streams != 1 || probe.windowChunks != profile.ProbeWindowChunks || probe.prefetchDepth != 0 || probe.cacheWrites {
		t.Fatalf("probe plan = %+v", probe)
	}
}

type planCaptureHost struct {
	entry drive.Entry
	plan  readPlan
	ok    bool
	reads int
}

func (h *planCaptureHost) Resolve(context.Context, string) (drive.Entry, error) {
	return h.entry, nil
}

func (*planCaptureHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{}, false, nil
}

func (*planCaptureHost) ReadCacheKey(drive.Entry) string { return "" }

func (h *planCaptureHost) DriverRead(ctx context.Context, _ drive.Entry, _, _ int64) (io.ReadCloser, error) {
	h.plan, h.ok = readPlanFromContext(ctx)
	h.reads++
	return io.NopCloser(strings.NewReader("x")), nil
}

func TestReadCarriesReadPlan(t *testing.T) {
	host := &planCaptureHost{entry: drive.Entry{ID: "id", Size: 1, ModTime: time.Now()}}
	profile := drive.DefaultReadProfile()
	profile.BulkStreams = 4
	profile.ParallelThreshold = 1
	reader := NewReader(ReaderDeps{Host: host, State: NewState(nil), Profile: profile})

	rc, err := reader.Read(WithReadIntent(context.Background(), ReadIntentBulk), "/file", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !host.ok {
		t.Fatal("read plan missing from driver read context")
	}
	if host.plan.intent != ReadIntentBulk || host.plan.streams != 4 || host.plan.cacheWrites {
		t.Fatalf("read plan = %+v, want bulk/4/no-cache", host.plan)
	}
}

func TestReadStreamCarriesReadPlan(t *testing.T) {
	host := &planCaptureHost{entry: drive.Entry{ID: "id", Size: 1, ModTime: time.Now()}}
	reader := NewReader(ReaderDeps{Host: host, State: NewState(nil), Profile: drive.DefaultReadProfile()})

	rc, err := reader.ReadStream(WithReadIntent(context.Background(), ReadIntentInteractive), "/file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !host.ok {
		t.Fatal("read plan missing from stream read context")
	}
	if host.plan.intent != ReadIntentInteractive || host.plan.streams != 1 || !host.plan.cacheWrites {
		t.Fatalf("stream plan = %+v, want interactive/single/cache", host.plan)
	}
}
