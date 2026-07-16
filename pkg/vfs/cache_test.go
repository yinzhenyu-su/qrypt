package vfs_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestCacheRecordPendingPermanentFailure(t *testing.T) {
	cache, err := vfs.NewCache(t.TempDir(), 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	pending := vfs.PendingFile{
		Path:      "/video.mp4",
		FID:       "video.mp4",
		ParentID:  "root",
		Name:      "video.mp4",
		LocalPath: "video.mp4.staging",
		Size:      10,
	}
	if err := cache.SavePending(pending); err != nil {
		t.Fatal(err)
	}

	got, ok, err := cache.RecordPendingPermanentFailure(pending.Path, errors.New("bad upload parameters"))
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

	cache, err := vfs.NewCache(cacheDir, 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	pending := cache.Pending()
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

func TestCacheCompactsPendingJournalDuringAppend(t *testing.T) {
	cacheDir := t.TempDir()
	cache, err := vfs.NewCache(cacheDir, 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	stagingDir := filepath.Join(cacheDir, "staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(stagingDir, "draft.txt.staging")
	if err := os.WriteFile(localPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending := vfs.PendingFile{
		Path:      "/draft.txt",
		FID:       "draft.txt",
		ParentID:  "root",
		Name:      "draft.txt",
		LocalPath: localPath,
		Size:      4,
	}
	if err := cache.SavePending(pending); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1100; i++ {
		got, ok, err := cache.RecordPendingFailure(pending.Path, errors.New("temporary failure"), 0)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatal("pending not found")
		}
		if got.RetryCount != i+1 {
			t.Fatalf("retry count = %d, want %d", got.RetryCount, i+1)
		}
	}
	journal, err := os.ReadFile(filepath.Join(cacheDir, "pending.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(journal), "\n"); lines > 128 {
		t.Fatalf("journal lines = %d, want compacted to at most 128", lines)
	}
	pendingFiles := cache.Pending()
	if len(pendingFiles) != 1 || pendingFiles[0].RetryCount != 1100 {
		t.Fatalf("pending = %+v, want one entry with retry_count=1100", pendingFiles)
	}
}
