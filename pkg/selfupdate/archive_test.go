package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// tarGzBytes builds a single-file gzip-compressed tar archive.
func tarGzBytes(t *testing.T, inner, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	header := &tar.Header{Name: inner, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipBytes builds a single-file zip archive.
func zipBytes(t *testing.T, inner, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(inner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A "./" prefix must still match on base name.
	archivePath := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(archivePath, tarGzBytes(t, "./qrypt-linux-amd64", "binary-payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out")
	if err := ExtractBinary(archivePath, FormatTarGz, "qrypt-linux-amd64", dst); err != nil {
		t.Fatalf("ExtractBinary error: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "binary-payload" {
		t.Fatalf("extracted content = %q", got)
	}
	if runtime.GOOS != "windows" {
		// Windows has no executable mode bit to assert.
		info, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("extracted binary is not executable: %v", info.Mode())
		}
	}
}

func TestExtractBinaryFromZip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "a.zip")
	if err := os.WriteFile(archivePath, zipBytes(t, "qrypt-windows-amd64.exe", "exe-payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.exe")
	if err := ExtractBinary(archivePath, FormatZip, "qrypt-windows-amd64.exe", dst); err != nil {
		t.Fatalf("ExtractBinary error: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "exe-payload" {
		t.Fatalf("extracted content = %q", got)
	}
}

func TestExtractBinaryMissingName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(archivePath, tarGzBytes(t, "qrypt-linux-amd64", "x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ExtractBinary(archivePath, FormatTarGz, "absent", filepath.Join(dir, "out"))
	if err == nil || !strings.Contains(err.Error(), "file does not exist") {
		t.Fatalf("ExtractBinary error = %v, want a not-exist error", err)
	}
}

func TestExtractBinaryUnknownFormat(t *testing.T) {
	t.Parallel()
	err := ExtractBinary("whatever", ArchiveFormat("rar"), "x", filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.Contains(err.Error(), "unsupported archive format") {
		t.Fatalf("ExtractBinary error = %v", err)
	}
}
