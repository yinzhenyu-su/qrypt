package subtree

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// recorder is a minimal vfs.FileSystem that records the namespace path each
// call receives.
type recorder struct {
	calls    []string
	invalCBs []func(string)
}

func (r *recorder) record(op, p string) { r.calls = append(r.calls, op+" "+p) }

func (r *recorder) Stat(_ context.Context, p string) (drive.Entry, error) {
	r.record("stat", p)
	return drive.Entry{Name: p}, nil
}
func (r *recorder) List(_ context.Context, p string) ([]drive.Entry, error) {
	r.record("list", p)
	return nil, nil
}
func (r *recorder) Read(_ context.Context, p string, _, _ int64) (io.ReadCloser, error) {
	r.record("read", p)
	return io.NopCloser(strings.NewReader("x")), nil
}
func (r *recorder) Create(_ context.Context, p string) error { r.record("create", p); return nil }
func (r *recorder) WriteAt(_ context.Context, p string, data []byte, _ int64) (int, error) {
	r.record("writeat", p)
	return len(data), nil
}
func (r *recorder) Flush(_ context.Context, p string) error { r.record("flush", p); return nil }
func (r *recorder) Mkdir(_ context.Context, p string) (drive.Entry, error) {
	r.record("mkdir", p)
	return drive.Entry{Name: p}, nil
}
func (r *recorder) Remove(_ context.Context, p string) error { r.record("remove", p); return nil }
func (r *recorder) RemoveDir(_ context.Context, p string) error {
	r.record("removedir", p)
	return nil
}
func (r *recorder) Rename(_ context.Context, oldPath, newPath string) error {
	r.record("rename", oldPath+" -> "+newPath)
	return nil
}
func (r *recorder) Truncate(_ context.Context, p string, _ int64) error {
	r.record("truncate", p)
	return nil
}
func (r *recorder) SubscribeInvalidations(cb func(string)) func() {
	r.invalCBs = append(r.invalCBs, cb)
	return func() {}
}

func lastCall(t *testing.T, r *recorder) string {
	t.Helper()
	if len(r.calls) == 0 {
		t.Fatal("no underlying call recorded")
	}
	return r.calls[len(r.calls)-1]
}

func TestRootViewPassesPathsThrough(t *testing.T) {
	rec := &recorder{}
	view, err := New(rec, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.Stat(context.Background(), "/a/b"); err != nil {
		t.Fatal(err)
	}
	if got := lastCall(t, rec); got != "stat /a/b" {
		t.Fatalf("call = %q", got)
	}
}

func TestNamedViewPrefixesAndClamps(t *testing.T) {
	rec := &recorder{}
	view, err := New(rec, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, _ = view.Stat(ctx, "/")
	if got := lastCall(t, rec); got != "stat /cloud" {
		t.Fatalf("root stat = %q", got)
	}
	_, _ = view.List(ctx, "/dir")
	if got := lastCall(t, rec); got != "list /cloud/dir" {
		t.Fatalf("list = %q", got)
	}
	_, _ = view.Stat(ctx, "/../etc/passwd")
	if got := lastCall(t, rec); got != "stat /cloud/etc/passwd" {
		t.Fatalf("clamped stat = %q", got)
	}
	if err := view.Rename(ctx, "/a", "/b"); err != nil {
		t.Fatal(err)
	}
	if got := lastCall(t, rec); got != "rename /cloud/a -> /cloud/b" {
		t.Fatalf("rename = %q", got)
	}
}

func TestNamedViewRejectsUnsafeNames(t *testing.T) {
	if _, err := New(&recorder{}, "a/b"); err == nil {
		t.Fatal("want error for slash in mount name")
	}
	if _, err := New(nil, "cloud"); err == nil {
		t.Fatal("want error for nil filesystem")
	}
}

func TestInvalidationPathsAreMappedAndFiltered(t *testing.T) {
	rec := &recorder{}
	view, err := New(rec, "cloud")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	view.SubscribeInvalidations(func(p string) { got = append(got, p) })
	if len(rec.invalCBs) != 1 {
		t.Fatalf("subscriptions = %d", len(rec.invalCBs))
	}
	rec.invalCBs[0]("/cloud")
	rec.invalCBs[0]("/cloud/dir/file.txt")
	rec.invalCBs[0]("/other/file.txt")
	want := []string{"/", "/dir/file.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("invalidations = %v, want %v", got, want)
	}
}
