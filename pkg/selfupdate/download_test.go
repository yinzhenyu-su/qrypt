package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadStreamsToDisk(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("qrypt", 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "asset")
	n, err := (&Downloader{HTTP: srv.Client()}).Download(context.Background(), srv.URL, dst)
	if err != nil {
		t.Fatalf("Download error: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("Download wrote %d bytes, want %d", n, len(payload))
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatal("downloaded content differs from the served payload")
	}
}

func TestDownloadReportsTruncation(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Declare more bytes than are written, then close early.
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()

	_, err := (&Downloader{HTTP: srv.Client()}).Download(context.Background(), srv.URL, filepath.Join(t.TempDir(), "asset"))
	if err == nil {
		t.Fatal("a body closed mid-transfer must be reported as an error, got nil")
	}
	if !strings.Contains(err.Error(), "download ") {
		t.Fatalf("error does not identify the download: %v", err)
	}
}

func TestDownloadReportsStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := (&Downloader{HTTP: srv.Client()}).Download(context.Background(), srv.URL, filepath.Join(t.TempDir(), "asset"))
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("Download error = %v, want a status error", err)
	}
}
