package drime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestLiveDrime is a real-account smoke test. It is skipped unless
// DRIME_LIVE_TOKEN is set; the token is never written to disk by this test.
//
//	DRIME_LIVE_TOKEN=... DRIME_LIVE_WORKSPACE=0 go test ./pkg/drivers/drime -run TestLiveDrime -v
func TestLiveDrime(t *testing.T) {
	token := os.Getenv("DRIME_LIVE_TOKEN")
	if token == "" {
		t.Skip("DRIME_LIVE_TOKEN is not set")
	}
	workspace := os.Getenv("DRIME_LIVE_WORKSPACE")
	if workspace == "" {
		workspace = "0"
	}
	ctx := context.Background()
	d := New(Options{
		AccessToken:       token,
		WorkspaceID:       workspace,
		ChunkSize:         minChunkSize,
		UploadConcurrency: 4,
		InstantUpload:     true,
		PresignSmall:      true,
		HardDelete:        true,
	})
	d.InstallStateStore(drive.NewFileStateStore(t.TempDir()))
	if err := d.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	run := strconv.FormatInt(time.Now().Unix(), 10)
	rootName := "qrypt-live-" + run
	root, err := d.Mkdir(ctx, "", rootName)
	if err != nil {
		t.Fatalf("Mkdir root: %v", err)
	}
	defer func() {
		if err := d.Remove(ctx, root); err != nil {
			t.Logf("cleanup root: %v", err)
		}
	}()

	listed, err := d.List(ctx, "")
	if err != nil {
		t.Fatalf("List root: %v", err)
	}
	found := false
	for _, entry := range listed {
		if entry.ID == root.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("created folder not listed")
	}
	if space, err := d.Space(ctx); err != nil || space.Total == 0 {
		t.Fatalf("Space = %+v, %v", space, err)
	}

	smallData := bytes.Repeat([]byte("small-"), 200)
	small, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: root.ID,
		Name:     "small.bin",
		Source:   drive.NewBytesReadOnlyFileSource(smallData),
		ModTime:  time.UnixMilli(1609459200000),
	})
	if err != nil {
		t.Fatalf("small upload: %v", err)
	}
	if small.Size != int64(len(smallData)) || small.Name != "small.bin" {
		t.Fatalf("small entry = %+v", small)
	}
	if hash := entryFileHash(small); len(hash) != 64 {
		t.Fatalf("file_hash = %q", hash)
	}
	if algo, hash, err := d.RemoteHash(ctx, small); err != nil || algo != drive.HashSHA256 || len(hash) != 64 {
		t.Fatalf("RemoteHash = %v %q %v", algo, hash, err)
	}
	instant, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: root.ID,
		Name:     "small-instant.bin",
		Source:   drive.NewBytesReadOnlyFileSource(smallData),
	})
	if err != nil {
		t.Fatalf("instant upload: %v", err)
	}
	if instant.Name != "small-instant.bin" {
		t.Fatalf("instant entry = %+v", instant)
	}
	snapshot, _ := d.DebugSnapshot(ctx)
	if count, _ := snapshot.Extra[drive.DebugExtraInstantUploadCount].(int64); count < 1 {
		t.Fatalf("instant upload counter = %v", snapshot.Extra[drive.DebugExtraInstantUploadCount])
	}
	specialName := " live\\name.bin "
	special, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: root.ID,
		Name:     specialName,
		Source:   drive.NewBytesReadOnlyFileSource([]byte("special")),
	})
	if err != nil {
		t.Fatalf("special-name upload: %v", err)
	}
	if special.Name != specialName {
		t.Fatalf("special name = %q, want %q", special.Name, specialName)
	}
	full, err := d.Read(ctx, small, 0, 0)
	if err != nil {
		t.Fatalf("Read full: %v", err)
	}
	got, _ := io.ReadAll(full)
	full.Close()
	if !bytes.Equal(got, smallData) {
		t.Fatalf("full read mismatch: %d/%d bytes", len(got), len(smallData))
	}
	part, err := d.Read(ctx, small, 10, 17)
	if err != nil {
		t.Fatalf("Read range: %v", err)
	}
	got, _ = io.ReadAll(part)
	part.Close()
	if !bytes.Equal(got, smallData[10:27]) {
		t.Fatalf("range read = %q", got)
	}

	largeData := bytes.Repeat([]byte("large-"), 1024*1024) // 6 MiB
	large, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: root.ID,
		Name:     "large.bin",
		Source:   drive.NewBytesReadOnlyFileSource(largeData),
	})
	if err != nil {
		t.Fatalf("large upload: %v", err)
	}
	if large.Size != int64(len(largeData)) {
		t.Fatalf("large size = %d", large.Size)
	}

	renamed, err := d.Rename(ctx, small, "small-renamed.bin")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	sub, err := d.Mkdir(ctx, root.ID, "sub")
	if err != nil {
		t.Fatalf("Mkdir sub: %v", err)
	}
	moved, err := d.Move(ctx, renamed, sub.ID)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	copied, err := d.Copy(ctx, moved, root.ID, "copy.bin")
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if copied.Name != "copy.bin" {
		t.Fatalf("copy name = %q", copied.Name)
	}
	if err := d.Remove(ctx, copied); err != nil {
		t.Fatalf("Remove copy: %v", err)
	}

	share, err := d.CreateShare(ctx, drive.ShareCreateRequest{
		Entries:  []drive.Entry{moved},
		Expiry:   24 * time.Hour,
		Passcode: "live-pass",
	})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if _, err := d.ListShares(ctx, drive.ShareListRequest{}); !isShareCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("ListShares = %v, want unsupported", err)
	}
	defer func() {
		if err := d.DeleteShare(ctx, share.LinkID); err != nil {
			t.Logf("cleanup share: %v", err)
		}
	}()
	opened, err := d.OpenShare(ctx, drive.ShareLink{LinkID: share.LinkID, Passcode: "live-pass"})
	if err != nil {
		t.Fatalf("OpenShare: %v", err)
	}
	page, err := d.ListShareEntries(ctx, opened, drive.SharePath{}, "")
	if err != nil {
		t.Fatalf("ListShareEntries: %v", err)
	}
	if len(page.Entries) != 0 {
		t.Fatalf("file share unexpectedly has children: %+v", page.Entries)
	}
	entry := moved
	entry.ID = entryHash(moved)
	targets, err := d.ShareDownloadTargets(ctx, drive.ShareFetchRequest{Session: opened, Entries: []drive.ShareEntry{{Entry: entry}}})
	if err != nil || len(targets) != 1 {
		t.Fatalf("ShareDownloadTargets = %+v, %v", targets, err)
	}

	if workspace != "0" {
		// The same tree was created inside the workspace; workspace-specific
		// isolation is already exercised by the list/upload above.
		if got, err := d.List(ctx, sub.ID); err != nil || len(got) != 1 {
			t.Fatalf("workspace sub listing = %+v, %v", got, err)
		}
	}

	if _, err := d.PutSource(ctx, drive.UploadRequest{Name: fmt.Sprintf("%0256d.bin", 0), Source: drive.NewBytesReadOnlyFileSource([]byte("x"))}); err == nil {
		t.Fatal("long name accepted")
	}
}
