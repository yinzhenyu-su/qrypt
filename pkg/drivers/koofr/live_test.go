package koofr

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
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
	// External mounts (e.g. Google Drive via KOOFR_LIVE_MOUNT_ID) reset the
	// mtime to server time on rename/move, so mtime-lineage assertions that
	// hold on the primary Koofr mount must be relaxed there.
	externalMount := false
	var mounts apiMountList
	if _, err := d.cl.get(ctx, "/mounts", nil, &mounts); err != nil {
		t.Fatalf("list mounts: %v", err)
	}
	for _, m := range mounts.Mounts {
		if m.ID == d.mountID {
			externalMount = !m.IsPrimary
		}
	}
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
		// The primary mount must preserve the source mtime across copy.
		// External mounts (e.g. Google Drive) already reset the mtime when
		// the file was renamed above, so the copy inherits the server time;
		// copy itself honors the modified parameter (verified by probe).
		if externalMount {
			t.Logf("copy mtime = %v, want %v (external mount reset mtime on rename; tolerated)", copied.ModTime, mod)
		} else {
			t.Fatalf("copy mtime = %v, want %v (copy must preserve mtime)", copied.ModTime, mod)
		}
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

// TestLiveKoofrVirtualRoot verifies the default aggregated root against the
// real account: the root lists the online external mounts (e.g. wgdrive)
// alongside the primary storage, writes inside an external mount route by
// "ext:" id, and copy works across mounts. Only a qrypt-vroot-<unix>
// temporary directory (primary + external side) is touched; both sides are
// removed and the trash emptied at the end.
func TestLiveKoofrVirtualRoot(t *testing.T) {
	email := os.Getenv("KOOFR_LIVE_EMAIL")
	password := os.Getenv("KOOFR_LIVE_PASSWORD")
	if email == "" || password == "" {
		t.Skip("KOOFR_LIVE_EMAIL/KOOFR_LIVE_PASSWORD are not set")
	}
	d := New(Options{
		Email:      email,
		Password:   password,
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

	// The virtual root must list every online external mount.
	root, err := d.List(ctx, "")
	if err != nil {
		t.Fatalf("List virtual root: %v", err)
	}
	var extEntry drive.Entry
	for _, entry := range root {
		if strings.HasPrefix(entry.ID, "ext:") && entry.IsDir {
			extEntry = entry
			break
		}
	}
	if extEntry.ID == "" {
		t.Fatal("virtual root lists no external mount directory")
	}
	t.Logf("virtual root lists external mount %q (id %s) among %d entries", extEntry.Name, extEntry.ID, len(root))

	run := strconv.FormatInt(time.Now().Unix(), 10)
	name := "qrypt-vroot-" + run
	staging, err := d.Mkdir(ctx, "", name)
	if err != nil {
		t.Fatalf("Mkdir primary staging: %v", err)
	}
	cleanup := func() {
		if err := d.Remove(ctx, staging); err != nil {
			t.Logf("cleanup primary staging: %v", err)
		}
		emptyTrash()
	}
	defer cleanup()

	// Create the same temporary directory inside the external mount.
	extDir, err := d.Mkdir(ctx, extEntry.ID, name)
	if err != nil {
		cleanup()
		t.Fatalf("Mkdir in external mount: %v", err)
	}
	if !strings.HasPrefix(extDir.ID, "ext:") {
		cleanup()
		t.Fatalf("ext mkdir id = %q, want ext: prefix", extDir.ID)
	}
	defer func() {
		if err := d.Remove(ctx, extDir); err != nil {
			t.Logf("cleanup ext dir: %v", err)
		}
	}()

	// Upload, read back, and rename inside the external mount.
	mod := time.UnixMilli(1700000000123).UTC()
	payload := []byte("qrypt-vroot-payload")
	file, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: extDir.ID,
		Name:     "data.bin",
		Source:   drive.NewBytesReadOnlyFileSource(payload),
		ModTime:  mod,
	})
	if err != nil {
		t.Fatalf("upload into external mount: %v", err)
	}
	if file.ID != joinPath(extDir.ID, "data.bin") {
		t.Logf("uploaded id = %q", file.ID)
	}
	reader, err := d.Read(ctx, file, 0, 0)
	if err != nil {
		t.Fatalf("Read ext file: %v", err)
	}
	got, _ := io.ReadAll(reader)
	reader.Close()
	if !bytes.Equal(got, payload) {
		t.Fatalf("ext read = %d bytes, want %d", len(got), len(payload))
	}
	renamed, err := d.Rename(ctx, file, "data-renamed.bin")
	if err != nil {
		t.Logf("rename in external mount: %v", err)
	} else if renamed.ModTime.UnixMilli() == mod.UnixMilli() {
		t.Logf("rename kept mtime %v (external mounts are known to reset it)", renamed.ModTime)
	} else {
		t.Logf("rename reset mtime to %v (known external-mount behavior)", renamed.ModTime)
	}

	// Cross-mount copy: external mount -> primary staging.
	src := file
	if renamed.ID != "" {
		src = renamed
	}
	copied, err := d.Copy(ctx, src, staging.ID, "cross-copy.bin")
	if err != nil {
		t.Logf("cross-mount copy: %v (provider may answer FilesNotSynced while the external mount is out of sync)", err)
	} else {
		if copied.ID != joinPath(staging.ID, "cross-copy.bin") || copied.Size != int64(len(payload)) {
			t.Fatalf("cross copy = %+v", copied)
		}
		t.Logf("cross-mount copy landed at %q (mtime %v)", copied.ID, copied.ModTime)
	}

	// Space reflects the primary mount.
	if space, err := d.Space(ctx); err != nil || space.Total == 0 {
		t.Fatalf("Space = %+v, %v", space, err)
	}
}

// TestLiveKoofrReviewChecks verifies the review fixes against the real
// provider: root_path isolation, case-only rename behavior, the anonymous
// path=/dir form for a link rooted at the mount root, and what a k00.fr
// short URL actually redirects to. Same credential gating and cleanup rules
// as TestLiveKoofr.
func TestLiveKoofrReviewChecks(t *testing.T) {
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
	run := strconv.FormatInt(time.Now().UnixNano(), 10)
	fixName := "qrypt-fix-" + run
	fixDir, err := d.Mkdir(ctx, "", fixName)
	if err != nil {
		t.Fatalf("Mkdir fix root: %v", err)
	}
	defer func() {
		if err := d.Remove(ctx, fixDir); err != nil {
			t.Logf("cleanup fix root: %v", err)
		}
		emptyTrash()
	}()

	// 1. root_path isolation: a second driver rooted at the temp directory
	// must resolve caller paths inside it and must not list outside it.
	rooted := New(Options{
		Email:      email,
		Password:   password,
		MountID:    d.mountID,
		APIBaseURL: os.Getenv("KOOFR_LIVE_BASE_URL"),
		RootPath:   fixDir.ID,
	})
	if err := rooted.Init(ctx); err != nil {
		t.Fatalf("rooted Init: %v", err)
	}
	defer func() { _ = rooted.Drop(ctx) }()
	sub, err := rooted.Mkdir(ctx, "", "sub")
	if err != nil {
		t.Fatalf("rooted Mkdir: %v", err)
	}
	resolved, err := rooted.ResolvePath(ctx, "/sub")
	if err != nil || resolved != joinPath(fixDir.ID, "sub") {
		t.Fatalf("rooted ResolvePath /sub = %q, %v, want %q", resolved, err, joinPath(fixDir.ID, "sub"))
	}
	if resolvedRoot, err := rooted.ResolvePath(ctx, "/"); err != nil || resolvedRoot != fixDir.ID {
		t.Fatalf("rooted ResolvePath / = %q, %v, want %q", resolvedRoot, err, fixDir.ID)
	}
	if _, err := rooted.ResolvePath(ctx, "/../escape"); err == nil {
		t.Fatal("rooted ResolvePath escaped the root")
	}
	listed, err := rooted.List(ctx, "")
	if err != nil {
		t.Fatalf("rooted List: %v", err)
	}
	for _, entry := range listed {
		if !isWithin(entry.ID, fixDir.ID) {
			t.Fatalf("rooted List leaked entry outside root_path: %q", entry.ID)
		}
	}
	if len(listed) != 1 || listed[0].ID != sub.ID {
		t.Fatalf("rooted List = %+v, want only %q", listed, sub.ID)
	}
	t.Logf("root_path isolation: ResolvePath and List stay inside %q", fixDir.ID)

	// 2. Case-only rename behavior on the live mount.
	caseEntry, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: fixDir.ID,
		Name:     "a.bin",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("case")),
		ModTime:  time.UnixMilli(1700000000123).UTC(),
	})
	if err != nil {
		t.Fatalf("upload a.bin: %v", err)
	}
	if _, err := d.Rename(ctx, caseEntry, "A.bin"); err != nil {
		t.Logf("case-only rename a.bin -> A.bin: error %v (provider rejects case-only renames)", err)
		if !errors.Is(err, drive.ErrConflict) {
			t.Logf("case-only rename error is not a conflict sentinel; record and reconsider mapping")
		}
	} else {
		t.Logf("case-only rename a.bin -> A.bin: succeeded (provider allows case-only renames)")
	}

	// 3. Root-path link: create a link on "/" directly and fetch a nested
	// file anonymously with the path=/dir/file form.
	rootLinkBody := map[string]any{"path": "/"}
	resp, err := d.cl.post(ctx, d.linksPath(""), nil, rootLinkBody, nil)
	if err != nil {
		t.Logf("root link create: %v (provider may refuse links on the mount root)", err)
	} else {
		var rootLink apiLink
		if err := json.Unmarshal(resp.body, &rootLink); err != nil || rootLink.ID == "" {
			t.Fatalf("root link create response undecodable: %v", err)
		}
		defer func() {
			if err := d.DeleteShare(ctx, rootLink.ID); err != nil {
				t.Logf("cleanup root link: %v", err)
			}
		}()
		nested, err := d.info(ctx, joinPath(fixDir.ID, "a.bin"))
		if err != nil {
			t.Fatalf("info a.bin: %v", err)
		}
		session, err := d.OpenShare(ctx, drive.ShareLink{LinkID: rootLink.ID})
		if err != nil {
			t.Fatalf("OpenShare root link: %v", err)
		}
		targets, err := d.ShareDownloadTargets(ctx, drive.ShareFetchRequest{
			Session: session,
			Entries: []drive.ShareEntry{{Entry: nested}},
		})
		if err != nil || len(targets) != 1 {
			t.Fatalf("ShareDownloadTargets = %+v, %v", targets, err)
		}
		targetURL, err := url.Parse(targets[0].URL)
		if err != nil {
			t.Fatal(err)
		}
		if got := targetURL.Query().Get("path"); got != joinPath(fixDir.ID, "a.bin") {
			t.Fatalf("root-link path query = %q, want %q", got, joinPath(fixDir.ID, "a.bin"))
		}
		anonReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targets[0].URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range targets[0].Headers {
			anonReq.Header.Set(name, value)
		}
		anonResp, err := (&http.Client{Timeout: 60 * time.Second}).Do(anonReq)
		if err != nil {
			t.Fatalf("root-link anonymous fetch: %v", err)
		}
		body, _ := io.ReadAll(anonResp.Body)
		anonResp.Body.Close()
		if anonResp.StatusCode != http.StatusOK || string(body) != "case" {
			t.Fatalf("root-link anonymous fetch = %d %q", anonResp.StatusCode, drive.Snippet(body))
		}
		t.Logf("root-path link anonymous download with path=%s form: OK", fixName+"/a.bin")
	}

	// 4. What does a k00.fr short URL redirect to?
	probeLink, err := d.CreateShare(ctx, drive.ShareCreateRequest{Entries: []drive.Entry{fixDir}})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	defer func() {
		if err := d.DeleteShare(ctx, probeLink.LinkID); err != nil {
			t.Logf("cleanup probe link: %v", err)
		}
	}()
	var apiProbeLink apiLink
	if _, err := d.cl.get(ctx, d.linksPath("/"+url.PathEscape(probeLink.LinkID)), nil, &apiProbeLink); err != nil {
		t.Fatalf("fetch link object: %v", err)
	}
	if apiProbeLink.ShortURL == "" {
		t.Skipf("link object has no shortUrl; cannot verify k00.fr redirect")
	}
	finalURL := ""
	redirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			finalURL = req.URL.String()
			return nil
		},
	}
	shortReq, err := http.NewRequestWithContext(ctx, http.MethodGet, apiProbeLink.ShortURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	shortResp, err := redirectClient.Do(shortReq)
	if err != nil {
		t.Logf("k00.fr short URL fetch: %v", err)
	} else {
		_, _ = io.Copy(io.Discard, shortResp.Body)
		shortResp.Body.Close()
		t.Logf("k00.fr %s (hash %q) redirects to %q (link id %q)", apiProbeLink.ShortURL, apiProbeLink.Hash, finalURL, probeLink.LinkID)
	}
	if link, ok := d.ParseShareLink(apiProbeLink.ShortURL); ok {
		t.Fatalf("ParseShareLink accepted a k00.fr short URL as %q; the hash is not a link id", link.LinkID)
	}
	if link, ok := d.ParseShareLink(apiProbeLink.URL); !ok || link.LinkID != probeLink.LinkID {
		t.Fatalf("ParseShareLink(%q) = %+v, %v", apiProbeLink.URL, link, ok)
	}
}

// TestLiveKoofrVirtualRootFix3 verifies the third review round against the
// real account: runtime mount-name hijack rejection under the virtual root,
// upload and overwrite regression inside an external mount, and cross-mount
// copy and move. Only qrypt-fix3-* temporary objects are created; both
// sides are removed and the trash emptied at the end.
func TestLiveKoofrVirtualRootFix3(t *testing.T) {
	email := os.Getenv("KOOFR_LIVE_EMAIL")
	password := os.Getenv("KOOFR_LIVE_PASSWORD")
	if email == "" || password == "" {
		t.Skip("KOOFR_LIVE_EMAIL/KOOFR_LIVE_PASSWORD are not set")
	}
	d := New(Options{
		Email:      email,
		Password:   password,
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

	root, err := d.List(ctx, "")
	if err != nil {
		t.Fatalf("List virtual root: %v", err)
	}
	var extEntry drive.Entry
	for _, entry := range root {
		if strings.HasPrefix(entry.ID, "ext:") && entry.IsDir {
			extEntry = entry
			break
		}
	}
	if extEntry.ID == "" {
		t.Fatal("virtual root lists no external mount directory")
	}

	// 1. Creating a root-level entry under the external mount's name must
	// be refused with a conflict (both casings).
	if _, err := d.Mkdir(ctx, "", extEntry.Name); err == nil || !errors.Is(err, drive.ErrConflict) {
		t.Fatalf("Mkdir(%q) error = %v, want conflict", extEntry.Name, err)
	}
	if _, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: "/",
		Name:     extEntry.Name,
		Source:   drive.NewBytesReadOnlyFileSource([]byte("x")),
	}); err == nil || !errors.Is(err, drive.ErrConflict) {
		t.Fatalf("upload named %q error = %v, want conflict", extEntry.Name, err)
	}
	t.Logf("runtime mount-name hijack under %q rejected with a conflict", extEntry.Name)

	// 2. Upload + overwrite regression inside the external mount.
	run := strconv.FormatInt(time.Now().UnixNano(), 10)
	extDir, err := d.Mkdir(ctx, extEntry.ID, "qrypt-fix3-"+run)
	if err != nil {
		t.Fatalf("Mkdir in external mount: %v", err)
	}
	defer func() {
		if err := d.Remove(ctx, extDir); err != nil {
			t.Logf("cleanup ext dir: %v", err)
		}
		emptyTrash()
	}()
	mod := time.UnixMilli(1700000000123).UTC()
	upload := func(name string, payload []byte, mtime time.Time) drive.Entry {
		t.Helper()
		file, err := d.PutSource(ctx, drive.UploadRequest{
			ParentID: extDir.ID,
			Name:     name,
			Source:   drive.NewBytesReadOnlyFileSource(payload),
			ModTime:  mtime,
		})
		if err != nil {
			t.Fatalf("upload %q into external mount: %v", name, err)
		}
		if file.Name != name {
			t.Fatalf("upload stored as %q, want %q", file.Name, name)
		}
		return file
	}
	upload("data.bin", []byte("first"), mod)
	overwritten := upload("data.bin", []byte("second"), mod)
	if overwritten.Size != int64(len("second")) {
		t.Fatalf("overwrite size = %d", overwritten.Size)
	}
	entries, err := d.List(ctx, extDir.ID)
	if err != nil {
		t.Fatalf("List ext dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "data.bin" {
		t.Fatalf("ext dir after overwrite = %+v, want exactly data.bin", entries)
	}
	t.Logf("external mount upload + overwrite regression: OK")

	// 3. Cross-mount copy and move regression (external -> primary ->
	// external round trip on temporary names).
	staging, err := d.Mkdir(ctx, "", "qrypt-fix3-"+run)
	if err != nil {
		t.Fatalf("Mkdir primary staging: %v", err)
	}
	defer func() {
		if err := d.Remove(ctx, staging); err != nil {
			t.Logf("cleanup primary staging: %v", err)
		}
	}()
	copied, err := d.Copy(ctx, overwritten, staging.ID, "cross-copy.bin")
	if err != nil {
		t.Fatalf("cross-mount copy: %v", err)
	}
	if copied.ID != joinPath(staging.ID, "cross-copy.bin") || copied.Size != overwritten.Size {
		t.Fatalf("cross copy = %+v", copied)
	}
	moved, err := d.Move(ctx, copied, extDir.ID)
	if err != nil {
		t.Fatalf("cross-mount move: %v", err)
	}
	if moved.ID != joinPath(extDir.ID, "cross-copy.bin") {
		t.Fatalf("cross move id = %q", moved.ID)
	}
	if err := d.Remove(ctx, moved); err != nil {
		t.Logf("cleanup moved file: %v", err)
	}
	t.Logf("cross-mount copy + move regression: OK")
}
