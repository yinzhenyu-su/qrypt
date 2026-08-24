package sftp

import (
	"context"
	"errors"
	"path"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func FuzzRootRelativePath(f *testing.F) {
	for _, seed := range []string{"", "/", "a/b", "a/../b", "../escape", "a/../../escape", "名字/#?.txt", "./a//b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got, err := rootRelativePath(input)
		if err != nil {
			if !errors.Is(err, drive.ErrInvalidInput) {
				t.Fatalf("rootRelativePath(%q) error = %v, want drive.ErrInvalidInput", input, err)
			}
			return
		}
		if strings.HasPrefix(got, "/") || (got != "" && path.Clean(got) != got) {
			t.Fatalf("rootRelativePath(%q) = %q, not a clean relative path", input, got)
		}
		if strings.Contains("/"+got+"/", "/../") {
			t.Fatalf("rootRelativePath(%q) = %q, contains parent traversal", input, got)
		}
	})
}

func FuzzResolvePathStaysWithinRoot(f *testing.F) {
	for _, seed := range []string{"/", "/docs/file.txt", "../escape", "/../../escape", "a/../b"} {
		f.Add(seed)
	}
	driver := New(Options{RootPath: "/srv/qrypt"})
	f.Fuzz(func(t *testing.T, input string) {
		got, err := driver.ResolvePath(context.Background(), input)
		if err != nil {
			if !errors.Is(err, drive.ErrInvalidInput) {
				t.Fatalf("ResolvePath(%q) error = %v, want drive.ErrInvalidInput", input, err)
			}
			return
		}
		if got != driver.rootPath && !strings.HasPrefix(got, driver.rootPath+"/") {
			t.Fatalf("ResolvePath(%q) = %q, escaped root %q", input, got, driver.rootPath)
		}
	})
}

func FuzzResolveIDStaysWithinRoot(f *testing.F) {
	for _, seed := range []string{"", "/", "0", "/srv/qrypt/file", "/tmp/escape", "child/file", "../escape"} {
		f.Add(seed)
	}
	driver := New(Options{RootPath: "/srv/qrypt"})
	f.Fuzz(func(t *testing.T, input string) {
		got, err := driver.resolveID(input)
		if err != nil {
			if !errors.Is(err, drive.ErrInvalidInput) {
				t.Fatalf("resolveID(%q) error = %v, want drive.ErrInvalidInput", input, err)
			}
			return
		}
		if got != driver.rootPath && !strings.HasPrefix(got, driver.rootPath+"/") {
			t.Fatalf("resolveID(%q) = %q, escaped root %q", input, got, driver.rootPath)
		}
	})
}

func FuzzValidateName(f *testing.F) {
	for _, seed := range []string{"", ".", "..", "file.txt", "a/b", "名字 #?.txt", "a\\b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		err := validateName(name)
		if err == nil && (name == "" || name == "." || name == ".." || path.Base(name) != name) {
			t.Fatalf("validateName(%q) accepted an unsafe name", name)
		}
		if err != nil && !errors.Is(err, drive.ErrInvalidInput) {
			t.Fatalf("validateName(%q) error = %v, want drive.ErrInvalidInput", name, err)
		}
	})
}

func FuzzReadRangeValidation(f *testing.F) {
	for _, seed := range []struct {
		offset int64
		size   int64
	}{{0, 0}, {0, 1}, {5, 7}, {-1, 1}, {1, -1}, {100, 0}} {
		f.Add(seed.offset, seed.size)
	}
	driver := New(Options{})
	entry := drive.Entry{ID: "/file", Size: 10}
	f.Fuzz(func(t *testing.T, offset, size int64) {
		reader, err := driver.Read(context.Background(), entry, offset, size)
		if offset < 0 || size < 0 {
			if !errors.Is(err, drive.ErrInvalidInput) {
				t.Fatalf("Read(%d, %d) error = %v, want drive.ErrInvalidInput", offset, size, err)
			}
			return
		}
		if offset >= entry.Size {
			if err != nil {
				t.Fatalf("Read(%d, %d) error = %v, want empty reader", offset, size, err)
			}
			if reader == nil {
				t.Fatal("Read returned nil reader without error")
			}
			reader.Close()
			return
		}
		if err == nil {
			reader.Close()
		}
	})
}
