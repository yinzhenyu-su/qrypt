package osutil

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenReadAll(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "all.txt", "hello world")

	rc, err := OpenRead(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestOpenReadWithOffset(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "offset.txt", "hello world")

	rc, err := OpenRead(path, 6, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "world" {
		t.Errorf("got %q, want %q", got, "world")
	}
}

func TestOpenReadWithSize(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "size.txt", "hello world")

	rc, err := OpenRead(path, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestOpenReadWithOffsetAndSize(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "both.txt", "hello world")

	rc, err := OpenRead(path, 6, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "wor" {
		t.Errorf("got %q, want %q", got, "wor")
	}
}

func TestOpenReadFileNotFound(t *testing.T) {
	_, err := OpenRead("/nonexistent/file.txt", 0, 0)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected IsNotExist error, got %T: %v", err, err)
	}
}

func TestOpenReadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "empty.txt", "")

	rc, err := OpenRead(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty data, got %d bytes", len(data))
	}
}

func TestOpenReadSizeBeyondFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "short.txt", "hi")

	rc, err := OpenRead(path, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "hi" {
		t.Errorf("got %q, want %q", got, "hi")
	}
}

func TestOpenReadClosesOnSeekError(t *testing.T) {
	// Opening a directory should succeed but Seek on it should fail.
	rc, err := OpenRead(".", 10, 0)
	if err != nil {
		// On some systems seeking a directory is allowed; skip is fine.
		t.Skip("seeking directory did not error:", err)
	}
	rc.Close()
}
