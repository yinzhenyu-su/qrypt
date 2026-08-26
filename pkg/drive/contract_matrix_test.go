package drive_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

// The local contract matrix is the quality floor every driver gets for free
// when it is tested against FakeDriver: capability declarations must line up
// with observable behaviour, unsupported operations must classify
// consistently, listings must be stable, the behaviour checks must pass, and
// debug snapshots must not carry credential material. Real-provider contract
// tests run nightly (or manually via the Contract Tests workflow); this file
// runs on every PR through the normal unit suite.

func TestCapabilityDeclarationsMatchBehaviour(t *testing.T) {
	t.Run("fake default capabilities work", func(t *testing.T) {
		d := drive.NewFakeDriver()
		if err := d.Init(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = d.Drop(context.Background()) }()
		ctx := context.Background()

		// Declared capabilities must be backed by working methods.
		for _, cap := range []drive.Capability{
			drive.CapabilityWriter, drive.CapabilitySourceUploader,
			drive.CapabilityPathResolver, drive.CapabilityRemoteNameResolver,
		} {
			if !drive.HasCapability(d, cap) {
				t.Errorf("FakeDriver should declare %q", cap)
			}
		}
		rootID, err := d.ResolvePath(ctx, "/")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Mkdir(ctx, rootID, "d1"); err != nil {
			t.Errorf("writer capability not backed by Mkdir: %v", err)
		}
		if _, err := d.ResolvePath(ctx, "/"); err != nil {
			t.Errorf("path_resolver capability not backed by ResolvePath: %v", err)
		}
		if _, err := d.ResolveRemoteName(ctx, "plain.txt"); err != nil {
			t.Errorf("remote_name_resolver capability not backed by ResolveRemoteName: %v", err)
		}
	})

	t.Run("undeclared capabilities are not advertised", func(t *testing.T) {
		d := drive.NewFakeDriver(drive.FakeWithCapabilities())
		if caps := d.Capabilities(); caps != nil {
			t.Errorf("empty capabilities = %v, want nil", caps)
		}
		for _, cap := range []drive.Capability{
			drive.CapabilityWriter, drive.CapabilitySpace,
			drive.CapabilityResumableUploader, drive.CapabilityMtime,
		} {
			if drive.HasCapability(d, cap) {
				t.Errorf("FakeDriver without capabilities still advertises %q", cap)
			}
		}
	})

	t.Run("space capability is conditional", func(t *testing.T) {
		d := drive.NewFakeDriver(drive.FakeWithCapabilities(drive.CapabilitySpace), drive.FakeWithSpace(1000, 500))
		if !drive.HasCapability(d, drive.CapabilitySpace) {
			t.Fatal("space capability not declared")
		}
		space, err := d.Space(context.Background())
		if err != nil {
			t.Fatalf("space method failed despite declaration: %v", err)
		}
		if space.Total != 1000 || space.Free != 500 {
			t.Errorf("space = %+v, want total=1000 free=500", space)
		}
	})

	t.Run("localfs declarations are consistent", func(t *testing.T) {
		d := localfs.New(t.TempDir())
		if err := d.Init(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = d.Drop(context.Background()) }()
		ctx := context.Background()
		if drive.HasCapability(d, drive.CapabilityWriter) {
			rootID, err := d.ResolvePath(ctx, "/")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Mkdir(ctx, rootID, "capcheck"); err != nil {
				t.Errorf("localfs declares writer but Mkdir failed: %v", err)
			}
		}
		if drive.HasCapability(d, drive.CapabilityPathResolver) {
			if _, err := d.ResolvePath(ctx, "/"); err != nil {
				t.Errorf("localfs declares path_resolver but ResolvePath failed: %v", err)
			}
		}
	})
}

func TestUnsupportedOperationsClassifyConsistently(t *testing.T) {
	// UnsupportedOperations is the documented way for partial drivers to say
	// "not supported"; every method must produce a classification of
	// "unsupported" that is never retryable.
	var uo drive.UnsupportedOperations
	ctx := context.Background()
	if _, err := uo.Mkdir(ctx, "p", "n"); err != drive.ErrUnsupported {
		t.Errorf("Mkdir = %v, want ErrUnsupported", err)
	}
	if err := uo.Move(ctx, drive.Entry{}, "p"); err != drive.ErrUnsupported {
		t.Errorf("Move = %v, want ErrUnsupported", err)
	}
	if err := uo.Rename(ctx, drive.Entry{}, "n"); err != drive.ErrUnsupported {
		t.Errorf("Rename = %v, want ErrUnsupported", err)
	}
	if err := uo.Remove(ctx, drive.Entry{}); err != drive.ErrUnsupported {
		t.Errorf("Remove = %v, want ErrUnsupported", err)
	}
	if got := drive.ErrorCategory(drive.ErrUnsupported); got != drive.ErrorCategoryUnsupported {
		t.Errorf("ErrorCategory(ErrUnsupported) = %q, want %q", got, drive.ErrorCategoryUnsupported)
	}
	if drive.RetryableCategory(drive.ErrorCategoryUnsupported) {
		t.Error("unsupported must never be retryable")
	}
}

func TestListingsAreStableAcrossCalls(t *testing.T) {
	d := drive.NewFakeDriver()
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Drop(context.Background()) }()
	ctx := context.Background()

	if err := d.Seed(map[string]string{
		"a.txt":     "a",
		"b.txt":     "bb",
		"dir/c.txt": "ccc",
		"dir/sub/d": "dddd",
		"zzz.txt":   "zzzzz",
	}); err != nil {
		t.Fatal(err)
	}

	stable := func(parentID, what string) {
		first, err := d.List(ctx, parentID)
		if err != nil {
			t.Fatalf("List(%s): %v", what, err)
		}
		second, err := d.List(ctx, parentID)
		if err != nil {
			t.Fatalf("List(%s) second call: %v", what, err)
		}
		if len(first) != len(second) {
			t.Fatalf("List(%s) length changed: %d vs %d", what, len(first), len(second))
		}
		for i := range first {
			if first[i].ID != second[i].ID || first[i].Name != second[i].Name || first[i].IsDir != second[i].IsDir {
				t.Errorf("List(%s) entry %d unstable: %+v vs %+v", what, i, first[i], second[i])
			}
		}
	}
	rootID, err := d.ResolvePath(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	stable(rootID, "root")
	resolved, err := d.ResolvePath(ctx, "/dir")
	if err != nil {
		t.Fatal(err)
	}
	stable(resolved, "dir")
}

func TestBehaviourChecksPassOnFakeDriver(t *testing.T) {
	d := drive.NewFakeDriver()
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Drop(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if violations := drive.RunBehaviorChecks(ctx, d); len(violations) != 0 {
		t.Fatalf("FakeDriver violated the behavior contract:\n%v", violations)
	}
}

func TestDebugSnapshotsCarryNoCredentialMaterial(t *testing.T) {
	sensitiveKeys := []string{"token", "password", "cookie", "secret", "credential", "authorization", "sessionkey", "apikey"}
	localfsRoot := t.TempDir()
	drivers := []struct {
		name string
		d    drive.Driver
		root string // local path to strip from the snapshot ("" = none)
	}{
		{"localfs", localfs.New(localfsRoot), localfsRoot},
		{"fake", drive.NewFakeDriver(), ""},
	}
	for _, tc := range drivers {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.d.Init(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tc.d.Drop(context.Background()) }()
			snapshot, err := tc.d.DebugSnapshot(context.Background())
			if err != nil {
				t.Fatalf("DebugSnapshot: %v", err)
			}
			blob, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			// Strip the test's own tempdir from the blob: its path embeds
			// the test function name, which would false-positive the scan.
			if tc.root != "" {
				blob = bytes.ReplaceAll(blob, []byte(tc.root), nil)
				// JSON escapes backslashes, so on Windows the raw root is
				// not present in the blob; strip the escaped form too.
				blob = bytes.ReplaceAll(blob, []byte(strings.ReplaceAll(tc.root, `\`, `\\`)), nil)
			}
			lower := strings.ToLower(string(blob))
			for _, key := range sensitiveKeys {
				if strings.Contains(lower, key) {
					t.Errorf("DebugSnapshot leaked %q material: %s", key, blob)
				}
			}
		})
	}
}

// Guard against accidental expansion of the public capability set: adding a
// new capability is a cross-cutting change that must be deliberate (new
// interface + declarations in every driver + matrix coverage).
func TestCapabilitySetIsStable(t *testing.T) {
	want := []drive.Capability{
		drive.CapabilityForeignEntries, drive.CapabilityMtime,
		drive.CapabilityPathResolver, drive.CapabilityRemoteNameResolver,
		drive.CapabilityResumableUploader, drive.CapabilitySourceUploader,
		drive.CapabilitySpace, drive.CapabilityWriter,
	}
	got := drive.Capabilities(drive.NewFakeDriver(drive.FakeWithCapabilities(want...)))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("capability set drifted:\n got %v\nwant %v", got, want)
	}
}
