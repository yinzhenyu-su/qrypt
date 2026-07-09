package fileutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")

	err := WriteAtomic(path, ".output-*", 0o600, false, func(file *os.File) error {
		_, err := file.WriteString("new")
		return err
	})
	if err != nil {
		t.Fatalf("WriteAtomic() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "new" {
		t.Fatalf("content = %q, want %q", got, "new")
	}
}

func TestWriteAtomicRejectsExistingFileWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	called := false
	err := WriteAtomic(path, ".output-*", 0o600, false, func(file *os.File) error {
		called = true
		return nil
	})
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("WriteAtomic() error = %v, want fs.ErrExist", err)
	}
	if called {
		t.Fatal("write callback called for an existing destination")
	}
}

func TestWriteAtomicReplacesExistingFileWithForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := WriteAtomic(path, ".output-*", 0o600, true, func(file *os.File) error {
		_, err := file.WriteString("new")
		return err
	})
	if err != nil {
		t.Fatalf("WriteAtomic() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "new" {
		t.Fatalf("content = %q, want %q", got, "new")
	}
}
