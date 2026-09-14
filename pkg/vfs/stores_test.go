package vfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newStoresInDir opens the upload/read-cache store pair under one temp dir;
// it is a test-only convenience for the production newStores constructor.
func newStoresInDir(dir string, maxSize int64) (*stores, error) {
	return newStores("test-mount", dir, filepath.Join(dir, "reading"), maxSize)
}

func TestCacheRecordUploadPermanentFailure(t *testing.T) {
	cache, err := newStoresInDir(t.TempDir(), 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	pending := PendingUpload{
		Path:      "/video.mp4",
		FID:       "video.mp4",
		ParentID:  "root",
		Name:      "video.mp4",
		LocalPath: "video.mp4.staging",
		Size:      10,
	}
	if err := cache.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}

	queued, ok := cache.UploadByPath(pending.Path)
	if !ok {
		t.Fatal("pending record missing after save")
	}
	got, ok, err := cache.RecordUploadPermanentFailureIfUnchanged(queued, errors.New("bad upload parameters"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("pending not found")
	}
	if !got.PermanentFail {
		t.Fatal("PermanentFail is false")
	}
	if got.NextAttemptAt != 0 {
		t.Fatalf("NextAttemptAt = %d, want 0", got.NextAttemptAt)
	}
	if got.RetryCount != 1 {
		t.Fatalf("RetryCount = %d, want 1", got.RetryCount)
	}
}

func TestCacheCompactsDuplicatePendingJournalOnLoad(t *testing.T) {
	cacheDir := t.TempDir()
	stagingDir := filepath.Join(cacheDir, "staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(stagingDir, "qrypt.log.staging")
	if err := os.WriteFile(localPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(cacheDir, "pending.jsonl")
	f, err := os.OpenFile(journalPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1100; i++ {
		line := fmt.Sprintf(
			`{"op":"dirty","path":"/qrypt.log","fid":"qrypt.log","parent_id":"root","name":"qrypt.log","local_path":%q,"size":%d,"updated_at":%d}`+"\n",
			localPath,
			i,
			i+1,
		)
		if _, err := f.WriteString(line); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	cache, err := newStoresInDir(cacheDir, 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	pending := cache.PendingUploads()
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}
	if pending[0].Size != 1099 {
		t.Fatalf("pending size = %d, want latest 1099", pending[0].Size)
	}
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(journal), "\n"); lines != 1 {
		t.Fatalf("journal lines after load compact = %d, want 1", lines)
	}
}

// Append-path journal compaction is pinned by TestJournalCompactsDuringAppend
// in pkg/vfs/upload/journal_ops_test.go, next to the store that owns the
// watermark. It lives there because stores embeds upload.PendingStore by type
// alias, so driving it through this package would only re-test that method -
// and reaching the production watermark here would mean 1024 fsyncs.
