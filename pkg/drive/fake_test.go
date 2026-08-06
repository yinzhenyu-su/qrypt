package drive

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"
	"time"
)

func newSeededFake(t *testing.T) *FakeDriver {
	t.Helper()
	d := NewFakeDriver()
	if err := d.Seed(map[string]string{
		"a.txt":      "aaa",
		"sub/b.txt":  "bbbbbb",
		"sub/deep/c": "cccc",
	}); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFakeListChildrenSorted(t *testing.T) {
	d := newSeededFake(t)
	rootID, err := d.ResolvePath(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := d.List(context.Background(), rootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("root children = %d, want 2 (a.txt, sub)", len(entries))
	}
	if entries[0].Name != "a.txt" || entries[1].Name != "sub" {
		t.Fatalf("root children order = %q, %q; want a.txt, sub", entries[0].Name, entries[1].Name)
	}
	if entries[0].Size != 3 {
		t.Fatalf("a.txt size = %d, want 3", entries[0].Size)
	}
}

func TestFakeReadOffsetsAndEOF(t *testing.T) {
	d := newSeededFake(t)
	entries, err := d.List(context.Background(), "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	// Full read.
	rc, err := d.Read(context.Background(), entries[0], 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "aaa" {
		t.Fatalf("full read = %q, want aaa", data)
	}
	// Offset read.
	rc, err = d.Read(context.Background(), entries[0], 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "a" {
		t.Fatalf("offset read = %q, want a", data)
	}
	// Offset beyond EOF yields empty, not error.
	rc, err = d.Read(context.Background(), entries[0], 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("EOF read = %q, want empty", data)
	}
}

func TestFakeErrorInjectionClassifies(t *testing.T) {
	d := NewFakeDriver()
	if _, err := d.PutSource(context.Background(), UploadRequest{
		ParentID: "0",
		Name:     "f.bin",
		Source:   NewBytesReadOnlyFileSource([]byte("payload")),
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := d.List(context.Background(), "0")
	if err != nil {
		t.Fatal(err)
	}
	d.ErrRead = fs.ErrPermission
	_, err = d.Read(context.Background(), entries[0], 0, -1)
	if err == nil {
		t.Fatal("expected injected read error")
	}
	if got := ErrorCategory(err); got != ErrorCategoryPermission {
		t.Fatalf("ErrorCategory(injected) = %q, want permission", got)
	}
	// One-shot: second read is clean again.
	if _, err := d.Read(context.Background(), entries[0], 0, -1); err != nil {
		t.Fatalf("injected error should clear after one use: %v", err)
	}
}

func TestFakeNotFoundClassification(t *testing.T) {
	d := NewFakeDriver()
	if _, err := d.List(context.Background(), "root/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("list missing = %v, want ErrNotFound", err)
	}
	if _, err := d.Read(context.Background(), Entry{ID: "root/missing"}, 0, -1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read missing = %v, want ErrNotFound", err)
	}
	if err := d.Remove(context.Background(), Entry{ID: "root/missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove missing = %v, want ErrNotFound", err)
	}
	if _, err := d.ResolvePath(context.Background(), "/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve missing = %v, want ErrNotFound", err)
	}
}

func TestFakeContextCancellation(t *testing.T) {
	d := NewFakeDriver()
	d.Delay = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := d.List(ctx, "0"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("list with deadline = %v, want DeadlineExceeded", err)
	}
}

func TestFakePutSourceAndModTime(t *testing.T) {
	d := NewFakeDriver()
	fixed := time.Unix(1_700_000_000, 0)
	entry, err := d.PutSource(context.Background(), UploadRequest{
		ParentID: "0",
		Name:     "up.bin",
		Source:   NewBytesReadOnlyFileSource([]byte("x")),
		ModTime:  fixed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Name != "up.bin" || entry.Size != 1 {
		t.Fatalf("put entry = %+v", entry)
	}
	entries, err := d.List(context.Background(), "0")
	if err != nil {
		t.Fatal(err)
	}
	var found *Entry
	for i := range entries {
		if entries[i].Name == "up.bin" {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatal("uploaded file not listed")
	}
}

func TestFakeCallLog(t *testing.T) {
	d := newSeededFake(t)
	if _, err := d.List(context.Background(), "0"); err != nil {
		t.Fatal(err)
	}
	calls := d.FakeCalls()
	// Seed itself does not go through the Driver interface, so the log
	// contains exactly the explicit calls made here.
	if len(calls) != 1 || calls[0] != "List:0" {
		t.Fatalf("call log = %v, want [List:0]", calls)
	}
}

func TestFakeSpaceCapability(t *testing.T) {
	d := NewFakeDriver()
	if _, err := d.Space(context.Background()); !errors.Is(err, ErrSpaceUnsupported) {
		t.Fatalf("space without capability = %v, want ErrSpaceUnsupported", err)
	}
	d = NewFakeDriver(FakeWithSpace(100, 40))
	sp, err := d.Space(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sp.Total != 100 || sp.Free != 40 {
		t.Fatalf("space = %+v, want total=100 free=40", sp)
	}
}
