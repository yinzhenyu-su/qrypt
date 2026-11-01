package koofr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func shareTarget(t *testing.T, f *fakeProvider, path string) drive.Entry {
	t.Helper()
	f.putFile(path, []byte("shared bytes"), 1700000000123)
	d := f.newDriver(t, Options{})
	entries, err := d.List(context.Background(), parentPath(path))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, entry := range entries {
		if entry.Name == baseName(path) {
			return entry
		}
	}
	t.Fatalf("target %s not found", path)
	return drive.Entry{}
}

// --- policy and creation -----------------------------------------------------

func TestSharePolicyDeclaration(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	policy := d.SharePolicy()
	if policy.Passcode != drive.PolicyOptional || !policy.List || !policy.Update || !policy.Download {
		t.Fatalf("policy = %+v", policy)
	}
	if policy.MaxEntries != 1 {
		t.Fatalf("MaxEntries = %d, want 1", policy.MaxEntries)
	}
	if !policy.SupportsExpiry(drive.ShareExpiryDay) || !policy.SupportsExpiry(drive.ShareExpiryPermanent) {
		t.Fatalf("expiries = %v", policy.Expiries)
	}
	if policy.Title || policy.Public || policy.Edit || policy.VisitLimit {
		t.Fatalf("policy declares unsupported options: %+v", policy)
	}
}

func TestCreateShareForSingleEntry(t *testing.T) {
	f := newFakeProvider(t)
	entry := shareTarget(t, f, "/shared.bin")
	d := f.newDriver(t, Options{})
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{entry}})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if share.LinkID == "" || share.URL == "" || share.Title != "shared.bin" {
		t.Fatalf("share = %+v", share)
	}
	if !share.Public {
		t.Fatalf("new link should not be passcode protected: %+v", share)
	}
	if got := f.requestsMatching("POST", "/links"); got != 1 {
		t.Fatalf("link creates = %d, want 1", got)
	}
}

func TestCreateShareRejectsMultipleEntries(t *testing.T) {
	f := newFakeProvider(t)
	entry := shareTarget(t, f, "/shared.bin")
	d := f.newDriver(t, Options{})
	_, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{entry, entry}})
	if !drive.IsShareErrorCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("CreateShare error = %v, want unsupported", err)
	}
}

func TestCreateShareWithPasscodeAndExpiry(t *testing.T) {
	f := newFakeProvider(t)
	entry := shareTarget(t, f, "/secret.bin")
	d := f.newDriver(t, Options{})
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{
		Entries:  []drive.Entry{entry},
		Passcode: "s3cret!",
		Expiry:   drive.ShareExpiryWeek,
	})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if share.Public || share.Passcode != "s3cret!" {
		t.Fatalf("share = %+v", share)
	}
	if share.ExpiresAt.IsZero() {
		t.Fatalf("share expiry missing: %+v", share)
	}
}

func TestCreateShareMissingPathRejected(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	_, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{
		Entries: []drive.Entry{{ID: "/gone.bin", Name: "gone.bin"}},
	})
	if !drive.IsShareErrorCode(err, drive.ShareErrProvider) && !drive.IsShareErrorCode(err, drive.ShareErrLinkInvalid) {
		t.Fatalf("CreateShare error = %v", err)
	}
}

// --- password hygiene ----------------------------------------------------------

func TestPasswordSetResponseNeverLeaked(t *testing.T) {
	f := newFakeProvider(t)
	entry := shareTarget(t, f, "/secret.bin")
	d := f.newDriver(t, Options{})
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{
		Entries:  []drive.Entry{entry},
		Passcode: "leak-me-not",
	})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatalf("DebugSnapshot: %v", err)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "leak-me-not") {
		t.Fatalf("snapshot leaks share passcode: %s", encoded)
	}

	// A failing password/set must not echo the plaintext either.
	f.faultStatus("PUT", "password/set", 3, http.StatusBadRequest, `{"error":{"code":"Other","message":"bad password \"leak-me-not\""}}`)
	_, err = d.CreateShare(context.Background(), drive.ShareCreateRequest{
		Entries:  []drive.Entry{entry},
		Passcode: "leak-me-not",
	})
	if err == nil {
		t.Fatalf("CreateShare error = nil, want failure")
	}
	if strings.Contains(err.Error(), "leak-me-not") {
		t.Fatalf("error leaks passcode: %v", err)
	}
	_ = share
}

// --- update / delete / list -----------------------------------------------------

func TestUpdateSharePasscodeRemovalUnsupported(t *testing.T) {
	f := newFakeProvider(t)
	entry := shareTarget(t, f, "/pw.bin")
	d := f.newDriver(t, Options{})
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{
		Entries:  []drive.Entry{entry},
		Passcode: "keep-me",
	})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	empty := ""
	_, err = d.UpdateShare(context.Background(), drive.ShareUpdateRequest{ID: share.LinkID, Passcode: &empty})
	if !drive.IsShareErrorCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("UpdateShare error = %v, want unsupported", err)
	}
}

func TestUpdateShareDownloadableAndPasscode(t *testing.T) {
	f := newFakeProvider(t)
	entry := shareTarget(t, f, "/upd.bin")
	d := f.newDriver(t, Options{})
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{entry}})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	updated, err := d.UpdateShare(context.Background(), drive.ShareUpdateRequest{ID: share.LinkID, Passcode: &[]string{"new-pass"}[0]})
	if err != nil {
		t.Fatalf("UpdateShare: %v", err)
	}
	if updated.Public || updated.Passcode != "new-pass" {
		t.Fatalf("updated = %+v", updated)
	}
	off := false
	updated, err = d.UpdateShare(context.Background(), drive.ShareUpdateRequest{ID: share.LinkID, Download: &off})
	if err != nil {
		t.Fatalf("UpdateShare download: %v", err)
	}
	if updated.ExpiresAt.IsZero() && updated.LinkID == "" {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestUpdateShareExpiry(t *testing.T) {
	f := newFakeProvider(t)
	entry := shareTarget(t, f, "/exp.bin")
	d := f.newDriver(t, Options{})
	share, _ := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{entry}})
	expiry := drive.ShareExpiryDay
	updated, err := d.UpdateShare(context.Background(), drive.ShareUpdateRequest{ID: share.LinkID, Expiry: &expiry})
	if err != nil {
		t.Fatalf("UpdateShare: %v", err)
	}
	if updated.ExpiresAt.IsZero() {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestDeleteShare(t *testing.T) {
	f := newFakeProvider(t)
	entry := shareTarget(t, f, "/del.bin")
	d := f.newDriver(t, Options{})
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{entry}})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if err := d.DeleteShare(context.Background(), share.LinkID); err != nil {
		t.Fatalf("DeleteShare: %v", err)
	}
	if _, err := d.GetShare(context.Background(), share.LinkID); !drive.IsShareErrorCode(err, drive.ShareErrNotFound) {
		t.Fatalf("GetShare error = %v, want not found", err)
	}
	if err := d.DeleteShare(context.Background(), share.LinkID); !drive.IsShareErrorCode(err, drive.ShareErrNotFound) {
		t.Fatalf("second DeleteShare error = %v, want not found", err)
	}
}

func TestListShares(t *testing.T) {
	f := newFakeProvider(t)
	entry := shareTarget(t, f, "/listed.bin")
	d := f.newDriver(t, Options{})
	first, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{entry}})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	page, err := d.ListShares(context.Background(), drive.ShareListRequest{})
	if err != nil {
		t.Fatalf("ListShares: %v", err)
	}
	found := false
	for _, share := range page.Shares {
		if share.LinkID == first.LinkID && share.URL != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("created link missing from listing: %+v", page.Shares)
	}
}

func TestGetShareMissing(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	_, err := d.GetShare(context.Background(), "link-999")
	if !drive.IsShareErrorCode(err, drive.ShareErrNotFound) {
		t.Fatalf("GetShare error = %v, want not found", err)
	}
}

func TestShareRateLimitMapped(t *testing.T) {
	f := newFakeProvider(t)
	entry := shareTarget(t, f, "/limited.bin")
	f.faultStatus("POST", "/links", 3, http.StatusTooManyRequests, `{"error":{"code":"Other","message":"too many links"}}`)
	d := f.newDriver(t, Options{})
	_, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{entry}})
	if !drive.IsShareErrorCode(err, drive.ShareErrRateLimited) {
		t.Fatalf("CreateShare error = %v, want rate limited", err)
	}
}

// --- link parsing -------------------------------------------------------------

func TestParseShareLink(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	cases := []struct {
		raw   string
		want  string
		valid bool
	}{
		{"https://app.koofr.net/links/abc123", "abc123", true},
		{"https://k00.fr/x1y2z3", "x1y2z3", true},
		{"https://example.com/links/abc123", "", false},
		{"https://app.koofr.net/other/abc123", "", false},
	}
	for _, tc := range cases {
		link, ok := d.ParseShareLink(tc.raw)
		if ok != tc.valid || link.LinkID != tc.want {
			t.Fatalf("ParseShareLink(%q) = %+v, %v", tc.raw, link, ok)
		}
	}
}

// --- receive ------------------------------------------------------------------

func TestOpenShareAndListEntries(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/bundle/root.bin", []byte("root bytes"), 1)
	f.putFile("/bundle/nested/deep.bin", []byte("deep bytes"), 1)
	d := f.newDriver(t, Options{})
	entries, _ := d.List(context.Background(), "/")
	var bundle drive.Entry
	for _, entry := range entries {
		if entry.Name == "bundle" {
			bundle = entry
		}
	}
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{bundle}})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	session, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: share.LinkID, URL: share.URL})
	if err != nil {
		t.Fatalf("OpenShare: %v", err)
	}
	page, err := d.ListShareEntries(context.Background(), session, drive.SharePath{}, "")
	if err != nil {
		t.Fatalf("ListShareEntries: %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("entries = %+v", page.Entries)
	}
	for _, entry := range page.Entries {
		if entry.RootID != "/bundle" {
			t.Fatalf("entry root = %q", entry.RootID)
		}
	}
}

func TestOpenShareUnknownLinkNotFound(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	_, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: "link-404"})
	if !drive.IsShareErrorCode(err, drive.ShareErrNotFound) {
		t.Fatalf("OpenShare error = %v, want not found", err)
	}
}

// --- fetch --------------------------------------------------------------------

func fetchSetup(t *testing.T, f *fakeProvider, passcode string) (drive.ShareSession, drive.ShareEntry) {
	t.Helper()
	f.putFile("/fetch/inner/deep.bin", []byte("exact deep bytes"), 1)
	d := f.newDriver(t, Options{})
	rootEntries, _ := d.List(context.Background(), "/")
	var dir drive.Entry
	for _, entry := range rootEntries {
		if entry.Name == "fetch" {
			dir = entry
		}
	}
	req := drive.ShareCreateRequest{Entries: []drive.Entry{dir}}
	if passcode != "" {
		req.Passcode = passcode
	}
	share, err := d.CreateShare(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	link := drive.ShareLink{LinkID: share.LinkID, URL: share.URL, Passcode: passcode}
	session, err := d.OpenShare(context.Background(), link)
	if err != nil {
		t.Fatalf("OpenShare: %v", err)
	}
	page, err := d.ListShareEntries(context.Background(), session, drive.SharePath{ID: "/fetch/inner"}, "")
	if err != nil {
		t.Fatalf("ListShareEntries: %v", err)
	}
	for _, entry := range page.Entries {
		if entry.Name == "deep.bin" {
			return session, entry
		}
	}
	t.Fatalf("deep.bin not found in share listing")
	return drive.ShareSession{}, drive.ShareEntry{}
}

func TestShareFetchAnonymousNestedFile(t *testing.T) {
	f := newFakeProvider(t)
	session, entry := fetchSetup(t, f, "")
	d := f.newDriver(t, Options{})
	targets, err := d.ShareDownloadTargets(context.Background(), drive.ShareFetchRequest{Session: session, Entries: []drive.ShareEntry{entry}})
	if err != nil {
		t.Fatalf("ShareDownloadTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %+v", targets)
	}
	// The anonymous route must not carry credentials.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, targets[0].URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("anonymous fetch: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(data) != "exact deep bytes" {
		t.Fatalf("anonymous fetch = %d %q", resp.StatusCode, data)
	}
}

func TestShareFetchPasscodeRequiredWithoutPasscode(t *testing.T) {
	f := newFakeProvider(t)
	session, entry := fetchSetup(t, f, "link-pass")
	session.Link.Passcode = "" // caller did not supply the passcode
	d := f.newDriver(t, Options{})
	_, err := d.ShareDownloadTargets(context.Background(), drive.ShareFetchRequest{Session: session, Entries: []drive.ShareEntry{entry}})
	if !drive.IsShareErrorCode(err, drive.ShareErrPasscodeRequired) {
		t.Fatalf("ShareDownloadTargets error = %v, want passcode required", err)
	}
}

func TestShareFetchWithPasscodeSucceeds(t *testing.T) {
	f := newFakeProvider(t)
	session, entry := fetchSetup(t, f, "link-pass")
	d := f.newDriver(t, Options{})
	targets, err := d.ShareDownloadTargets(context.Background(), drive.ShareFetchRequest{Session: session, Entries: []drive.ShareEntry{entry}})
	if err != nil {
		t.Fatalf("ShareDownloadTargets: %v", err)
	}
	resp, err := http.Get(targets[0].URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(data) != "exact deep bytes" {
		t.Fatalf("fetch = %d %q", resp.StatusCode, data)
	}
}

func TestShareFetchRejectsDirectory(t *testing.T) {
	f := newFakeProvider(t)
	session, _ := fetchSetup(t, f, "")
	d := f.newDriver(t, Options{})
	_, err := d.ShareDownloadTargets(context.Background(), drive.ShareFetchRequest{
		Session: session,
		Entries: []drive.ShareEntry{{Entry: drive.Entry{ID: "/fetch/inner", Name: "inner", IsDir: true}}},
	})
	if !drive.IsShareErrorCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("ShareDownloadTargets error = %v, want unsupported", err)
	}
}

func TestShareFetchNonDownloadableRejected(t *testing.T) {
	f := newFakeProvider(t)
	session, entry := fetchSetup(t, f, "")
	d := f.newDriver(t, Options{})
	if _, err := d.UpdateShare(context.Background(), drive.ShareUpdateRequest{ID: session.Link.LinkID, Download: &[]bool{false}[0]}); err != nil {
		t.Fatalf("UpdateShare: %v", err)
	}
	// Re-open to refresh the session's downloadable flag.
	fresh, err := d.OpenShare(context.Background(), session.Link)
	if err != nil {
		t.Fatalf("OpenShare: %v", err)
	}
	_, err = d.ShareDownloadTargets(context.Background(), drive.ShareFetchRequest{Session: fresh, Entries: []drive.ShareEntry{entry}})
	if !drive.IsShareErrorCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("ShareDownloadTargets error = %v, want unsupported", err)
	}
}

// --- negative contract -----------------------------------------------------------

func TestShareSaveNotImplemented(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	if _, ok := interface{}(d).(drive.ShareSaver); ok {
		t.Fatalf("koofr must not implement ShareSaver")
	}
	if drive.HasCapability(d, drive.CapabilityShareSave) {
		t.Fatalf("koofr must not declare share_save")
	}
}

func TestStandaloneMtimeUnsupported(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	// The provider only accepts mtime at upload/copy time; there is no
	// set-mtime operation to (not) call, but the driver must not pretend.
	if drive.HasCapability(d, drive.CapabilityResumableUploader) {
		t.Fatalf("koofr must not declare resumable_uploader")
	}
}
