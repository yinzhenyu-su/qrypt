package koofr

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestLiveKoofr is a real-account smoke test against app.koofr.net (or
// KOOFR_LIVE_BASE_URL). It is skipped unless both KOOFR_LIVE_EMAIL and
// KOOFR_LIVE_PASSWORD are set; credentials are never written to disk,
// logs, or error messages by this test.
//
//	KOOFR_LIVE_EMAIL=... KOOFR_LIVE_PASSWORD=... \
//	KOOFR_LIVE_MOUNT_ID=... KOOFR_LIVE_BASE_URL=... \
//	go test ./pkg/drivers/koofr -run TestLiveKoofr -v -count=1
//
// The test only touches a qrypt-live-<unix> temporary directory, removes it
// at the end, and empties the account trash afterwards.
func TestLiveKoofr(t *testing.T) {
	email := os.Getenv("KOOFR_LIVE_EMAIL")
	password := os.Getenv("KOOFR_LIVE_PASSWORD")
	if email == "" || password == "" {
		t.Skip("KOOFR_LIVE_EMAIL/KOOFR_LIVE_PASSWORD are not set")
	}
	d := New(Options{
		Email:      email,
		Password:   password,
		MountID:    os.Getenv("KOOFR_LIVE_MOUNT_ID"),
		APIBaseURL: os.Getenv("KOOFR_LIVE_BASE_URL"),
	})
	ctx := context.Background()
	if err := d.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = d.Drop(ctx) }()
	emptyTrash := func() {
		if _, err := d.cl.delete(ctx, "/trash", nil, nil); err != nil {
			t.Logf("empty trash: %v", err)
		}
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
		emptyTrash()
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

	// mkdir is idempotent: a repeated create returns the existing directory.
	again, err := d.Mkdir(ctx, "", rootName)
	if err != nil {
		t.Fatalf("Mkdir idempotent: %v", err)
	}
	if again.ID != root.ID {
		t.Fatalf("mkdir idempotent id = %q, want %q", again.ID, root.ID)
	}

	if space, err := d.Space(ctx); err != nil || space.Total == 0 {
		t.Fatalf("Space = %+v, %v", space, err)
	}

	// Upload pins mtime and overwrite semantics; verify both round-trip.
	mod := time.UnixMilli(1700000000123).UTC()
	data := bytes.Repeat([]byte("koofr-live-"), 100)
	file, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: root.ID,
		Name:     "data.bin",
		Source:   drive.NewBytesReadOnlyFileSource(data),
		ModTime:  mod,
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if file.Size != int64(len(data)) || file.Name != "data.bin" {
		t.Fatalf("upload entry = %+v", file)
	}
	if file.ModTime.UnixMilli() != mod.UnixMilli() {
		t.Fatalf("upload mtime = %v, want %v", file.ModTime, mod)
	}
	sum := md5.Sum(data)
	if algo, hash, err := d.RemoteHash(ctx, file); err != nil || algo != drive.HashMD5 || hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("RemoteHash = %v %q %v", algo, hash, err)
	}

	full, err := d.Read(ctx, file, 0, 0)
	if err != nil {
		t.Fatalf("Read full: %v", err)
	}
	got, _ := io.ReadAll(full)
	full.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("full read mismatch: %d/%d bytes", len(got), len(data))
	}
	part, err := d.Read(ctx, file, 10, 17)
	if err != nil {
		t.Fatalf("Read range: %v", err)
	}
	got, _ = io.ReadAll(part)
	part.Close()
	if !bytes.Equal(got, data[10:27]) {
		t.Fatalf("range read mismatch: %d bytes", len(got))
	}

	renamed, err := d.Rename(ctx, file, "data-renamed.bin")
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
	if moved.ID != joinPath(sub.ID, "data-renamed.bin") {
		t.Fatalf("move id = %q", moved.ID)
	}
	copied, err := d.Copy(ctx, moved, root.ID, "copy.bin")
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if copied.ModTime.UnixMilli() != mod.UnixMilli() {
		t.Fatalf("copy mtime = %v, want %v (copy must preserve mtime)", copied.ModTime, mod)
	}
	if err := d.Remove(ctx, copied); err != nil {
		t.Fatalf("Remove copy: %v", err)
	}
	entries, err := d.List(ctx, root.ID)
	if err != nil {
		t.Fatalf("List after remove: %v", err)
	}
	for _, entry := range entries {
		if entry.Name == "copy.bin" {
			t.Fatal("removed copy still listed")
		}
	}

	// Share create/list/delete against the real account.
	share, err := d.CreateShare(ctx, drive.ShareCreateRequest{
		Entries:  []drive.Entry{moved},
		Passcode: "l1v3p1",
	})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	defer func() {
		if err := d.DeleteShare(ctx, share.LinkID); err != nil {
			t.Logf("cleanup share: %v", err)
		}
	}()
	if share.Passcode != "l1v3p1" || share.URL == "" {
		t.Fatalf("share = %+v", share)
	}
	page, err := d.ListShares(ctx, drive.ShareListRequest{})
	if err != nil {
		t.Fatalf("ListShares: %v", err)
	}
	listedShare := false
	for _, item := range page.Shares {
		if item.LinkID == share.LinkID {
			listedShare = true
		}
	}
	if !listedShare {
		t.Fatalf("created link %q not listed (%d links)", share.LinkID, len(page.Shares))
	}

	// M2 open question 1: anonymous content-link fetch with the passcode as a
	// `password` query parameter (no authentication) must return the raw bytes.
	session, err := d.OpenShare(ctx, drive.ShareLink{LinkID: share.LinkID, Passcode: "l1v3p1"})
	if err != nil {
		t.Fatalf("OpenShare: %v", err)
	}
	targets, err := d.ShareDownloadTargets(ctx, drive.ShareFetchRequest{
		Session: session,
		Entries: []drive.ShareEntry{{Entry: moved}},
	})
	if err != nil || len(targets) != 1 {
		t.Fatalf("ShareDownloadTargets = %+v, %v", targets, err)
	}
	anon := &http.Client{Timeout: 60 * time.Second}
	anonReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targets[0].URL, nil)
	if err != nil {
		t.Fatalf("build anonymous request: %v", err)
	}
	for name, value := range targets[0].Headers {
		anonReq.Header.Set(name, value)
	}
	resp, err := anon.Do(anonReq)
	if err != nil {
		t.Fatalf("anonymous fetch: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous password fetch status = %d body %q (query password form rejected)", resp.StatusCode, drive.Snippet(body))
	}
	if !bytes.Equal(body, data) {
		t.Fatalf("anonymous fetch mismatch: %d/%d bytes", len(body), len(data))
	}
	t.Logf("anonymous fetch with password query param: %d bytes, status %d", len(body), resp.StatusCode)
	// Without the passcode the same anonymous fetch must be refused.
	bare, err := url.Parse(targets[0].URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	q := bare.Query()
	q.Del("password")
	bare.RawQuery = q.Encode()
	bareReq, err := http.NewRequestWithContext(ctx, http.MethodGet, bare.String(), nil)
	if err != nil {
		t.Fatalf("build bare request: %v", err)
	}
	for name, value := range targets[0].Headers {
		bareReq.Header.Set(name, value)
	}
	resp, err = anon.Do(bareReq)
	if err != nil {
		t.Fatalf("anonymous fetch without password: %v", err)
	}
	noPass, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK && len(noPass) == len(data) {
		t.Fatal("anonymous fetch succeeded without the passcode")
	}
	t.Logf("anonymous fetch without password: status %d", resp.StatusCode)

	// M2 open question 2: the shape of the account-level GET /api/v2.1/links.
	raw, err := d.cl.get(ctx, "/links", nil, nil)
	if err != nil {
		t.Logf("account-level /api/v2.1/links: %v (implementation uses the mount-scoped endpoint)", err)
	} else {
		trimmed := bytes.TrimSpace(raw.body)
		shape := "object"
		var count int
		if len(trimmed) > 0 && trimmed[0] == '[' {
			shape = "array"
			var arr []apiLink
			_ = json.Unmarshal(trimmed, &arr)
			count = len(arr)
		} else {
			var enveloped struct {
				Links []apiLink `json:"links"`
			}
			_ = json.Unmarshal(trimmed, &enveloped)
			count = len(enveloped.Links)
		}
		t.Logf("account-level /api/v2.1/links returns a JSON %s (%d links)", shape, count)
	}
}
