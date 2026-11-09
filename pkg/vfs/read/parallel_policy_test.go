package read

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

type policyDriver struct {
	*drive.FakeDriver
	caps        []drive.Capability
	unsupported bool
	strictCalls atomic.Int32
	plainCalls  atomic.Int32
}

func (d *policyDriver) Capabilities() []drive.Capability { return d.caps }

func (d *policyDriver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if drive.StrictRangeFromContext(ctx) {
		d.strictCalls.Add(1)
		if d.unsupported {
			return nil, drive.ErrUnsupported
		}
	} else {
		d.plainCalls.Add(1)
	}
	return d.FakeDriver.Read(ctx, entry, offset, size)
}

type policyHost struct {
	driver   drive.Driver
	entry    drive.Entry
	cacheKey string
}

func (h *policyHost) Resolve(context.Context, string) (drive.Entry, error) { return h.entry, nil }

func (*policyHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{}, false, nil
}

func (h *policyHost) ReadCacheKey(drive.Entry) string { return h.cacheKey }

func (h *policyHost) DriverRead(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	return h.driver.Read(ctx, entry, offset, size)
}

func classifyReadPolicy(strict, plain int32) string {
	switch {
	case strict > 0 && plain == 0:
		return "parallel"
	case strict > 0 && plain > 0:
		return "fallback"
	case strict == 0 && plain > 0:
		return "single"
	default:
		return "none"
	}
}

func TestParallelPolicyMatchesDirectRead(t *testing.T) {
	cases := []struct {
		name        string
		caps        []drive.Capability
		unsupported bool
		want        string
	}{
		{"parallel", []drive.Capability{drive.CapabilityParallelRange}, false, "parallel"},
		{"no-capability", nil, false, "single"},
		{"internal-parallel", []drive.Capability{drive.CapabilityParallelRange, drive.CapabilityInternalParallelRead}, false, "single"},
		{"strict-unsupported", []drive.Capability{drive.CapabilityParallelRange}, true, "fallback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := drive.NewFakeDriver()
			content := make([]byte, 16<<20)
			if err := base.Seed(map[string]string{"file.bin": string(content)}); err != nil {
				t.Fatalf("Seed: %v", err)
			}
			driver := &policyDriver{FakeDriver: base, caps: tc.caps, unsupported: tc.unsupported}
			entry := drive.Entry{
				ID:      "0/file.bin",
				Name:    "file.bin",
				Size:    int64(len(content)),
				ModTime: time.Unix(1700000000, 0),
			}

			reader := NewReader(ReaderDeps{
				Host:    &policyHost{driver: driver, entry: entry, cacheKey: "cache"},
				State:   NewState(nil),
				Profile: drive.ReadProfileOf(driver),
			})
			ctx := WithoutReadPrefetch(WithReadIntent(context.Background(), ReadIntentBulk))
			rc, err := reader.Read(ctx, "/file.bin", 0, int64(len(content)))
			if err != nil {
				t.Fatalf("VFS read: %v", err)
			}
			if _, err := io.ReadAll(rc); err != nil {
				t.Fatalf("VFS read body: %v", err)
			}
			rc.Close()
			vfsStrict, vfsPlain := driver.strictCalls.Load(), driver.plainCalls.Load()

			driver.strictCalls.Store(0)
			driver.plainCalls.Store(0)
			rc, err = drive.ReadParallel(context.Background(), driver, entry, 0, int64(len(content)))
			if err != nil {
				t.Fatalf("direct read: %v", err)
			}
			if _, err := io.ReadAll(rc); err != nil {
				t.Fatalf("direct read body: %v", err)
			}
			rc.Close()
			directStrict, directPlain := driver.strictCalls.Load(), driver.plainCalls.Load()

			if got := classifyReadPolicy(vfsStrict, vfsPlain); got != tc.want {
				t.Fatalf("VFS policy = %s (strict=%d plain=%d), want %s", got, vfsStrict, vfsPlain, tc.want)
			}
			if got := classifyReadPolicy(directStrict, directPlain); got != tc.want {
				t.Fatalf("direct policy = %s (strict=%d plain=%d), want %s", got, directStrict, directPlain, tc.want)
			}
		})
	}
}
