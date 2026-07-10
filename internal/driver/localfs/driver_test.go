package localfs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestDriverInitRequiresExistingDirectory(t *testing.T) {
	ctx := context.Background()
	if err := New(filepath.Join(t.TempDir(), "missing")).Init(ctx); err == nil {
		t.Fatal("expected missing root_path to fail")
	}

	root := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(root, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := New(root).Init(ctx); err == nil {
		t.Fatal("expected file root to fail")
	}
}

func TestDriverFileOperations(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	driver := New(root)
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}

	docs, err := driver.Mkdir(ctx, "", "docs")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := driver.PutSource(ctx, drive.UploadRequest{
		ParentID: docs.ID,
		Name:     "note.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("hello world")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size != int64(len("hello world")) {
		t.Fatalf("entry size = %d, want %d", entry.Size, len("hello world"))
	}

	rc, err := driver.Read(ctx, entry, 6, 5)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world" {
		t.Fatalf("read data = %q, want world", data)
	}

	if err := driver.Rename(ctx, entry, "renamed.txt"); err != nil {
		t.Fatal(err)
	}
	renamed := drive.Entry{ID: filepath.Join(docs.ID, "renamed.txt"), Name: "renamed.txt"}
	archive, err := driver.Mkdir(ctx, "", "archive")
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Move(ctx, renamed, archive.ID); err != nil {
		t.Fatal(err)
	}

	entries, err := driver.List(ctx, archive.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, item := range entries {
		names = append(names, item.Name)
	}
	if !slices.Contains(names, "renamed.txt") {
		t.Fatalf("archive entries = %#v, want renamed.txt", names)
	}

	if err := driver.Remove(ctx, drive.Entry{ID: filepath.Join(archive.ID, "renamed.txt")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(archive.ID, "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected moved file to be removed, stat err = %v", err)
	}

	if err := driver.Remove(ctx, docs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(docs.ID); !os.IsNotExist(err) {
		t.Fatalf("expected directory to be removed, stat err = %v", err)
	}
}

func TestDriverPutOverwritesExistingFile(t *testing.T) {
	ctx := context.Background()
	driver := New(t.TempDir())

	if _, err := driver.PutSource(ctx, drive.UploadRequest{
		ParentID: "",
		Name:     "same.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("one")),
	}); err != nil {
		t.Fatal(err)
	}
	entry, err := driver.PutSource(ctx, drive.UploadRequest{
		ParentID: "",
		Name:     "same.txt",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("two")),
	})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := driver.Read(ctx, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "two" {
		t.Fatalf("file content = %q, want two", data)
	}
}

func TestDriverResolvePathStaysInsideRoot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	driver := New(root)

	got, err := driver.ResolvePath(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("root path = %q, want %q", got, root)
	}

	nested, err := driver.ResolvePath(ctx, "dir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if nested != filepath.Join(root, "dir", "file.txt") {
		t.Fatalf("nested path = %q", nested)
	}

	absoluteInside := filepath.Join(root, "inside.txt")
	got, err = driver.ResolvePath(ctx, absoluteInside)
	if err != nil {
		t.Fatal(err)
	}
	if got != absoluteInside {
		t.Fatalf("absolute inside path = %q, want %q", got, absoluteInside)
	}

	if _, err := driver.ResolvePath(ctx, "../escape.txt"); err == nil {
		t.Fatal("expected relative path escape to fail")
	}
	if _, err := driver.ResolvePath(ctx, filepath.Dir(root)); err == nil {
		t.Fatal("expected absolute path outside root to fail")
	}
}

func TestDriverDebugAndHealth(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	driver := New(root)

	snapshot, err := driver.DebugSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Driver != "localfs" || snapshot.Health != "ok" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if snapshot.Stats["root_path"] != root {
		t.Fatalf("snapshot root = %#v, want %q", snapshot.Stats["root_path"], root)
	}

}

func TestDriverResolveRemoteNameIsIdentity(t *testing.T) {
	info, err := New(t.TempDir()).ResolveRemoteName(context.Background(), "plain.txt")
	if err != nil {
		t.Fatal(err)
	}
	if info.PlainName != "plain.txt" || info.RemoteName != "plain.txt" {
		t.Fatalf("unexpected remote name info: %#v", info)
	}
}

func TestDriverSpace(t *testing.T) {
	ctx := context.Background()
	driver := New(t.TempDir())
	if err := driver.Init(ctx); err != nil {
		t.Fatal(err)
	}

	space, err := driver.Space(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if space.Total <= 0 {
		t.Fatalf("expected positive total space, got %d", space.Total)
	}
	if space.Free <= 0 {
		t.Fatalf("expected positive free space, got %d", space.Free)
	}
	if space.Free > space.Total {
		t.Fatalf("free space %d exceeds total space %d", space.Free, space.Total)
	}
}

var _ drive.SpaceQuerier = (*Driver)(nil)
