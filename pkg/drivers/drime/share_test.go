package drime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestSharePolicyAndLinkManagement(t *testing.T) {
	f := newFakeProvider(t)
	file := f.addEntry(0, "shared.bin", false, 42, "", strings.Repeat("a", 64))
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	policy := d.SharePolicy()
	if policy.List || !policy.Update || policy.Passcode != drive.PolicyOptional || !policy.Selection {
		t.Fatalf("policy = %+v", policy)
	}
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{
		Entries:  []drive.Entry{file.entry("")},
		Expiry:   7 * 24 * time.Hour,
		Passcode: "s3cret",
	})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if share.LinkID != "link-"+idString(file.ID) || share.NativeID != idString(file.ID) {
		t.Fatalf("share = %+v", share)
	}
	if !strings.HasPrefix(share.URL, "https://dri.me/") || share.ExpiresAt.IsZero() {
		t.Fatalf("share URL/expiry = %q %v", share.URL, share.ExpiresAt)
	}
	if share.Passcode != "s3cret" {
		t.Fatalf("passcode = %q", share.Passcode)
	}

	if _, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{file.entry(""), file.entry("")}}); !isShareCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("multi-entry error = %v", err)
	}
	if _, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{file.entry("")}, Title: "x"}); !isShareCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("title error = %v", err)
	}
	if _, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{file.entry("")}, VisitLimit: 3}); !isShareCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("visit-limit error = %v", err)
	}
	if _, err := d.ListShares(context.Background(), drive.ShareListRequest{}); !isShareCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("ListShares error = %v", err)
	}

	got, err := d.GetShare(context.Background(), share.LinkID)
	if err != nil {
		t.Fatalf("GetShare: %v", err)
	}
	if got.LinkID != share.LinkID || got.NativeID != share.NativeID {
		t.Fatalf("GetShare = %+v", got)
	}
	newPass := "newpass"
	if _, err := d.UpdateShare(context.Background(), drive.ShareUpdateRequest{ID: share.LinkID, Passcode: &newPass}); err != nil {
		t.Fatalf("UpdateShare: %v", err)
	}
	if err := d.DeleteShare(context.Background(), share.LinkID); err != nil {
		t.Fatalf("DeleteShare: %v", err)
	}
	if _, err := d.GetShare(context.Background(), share.LinkID); !isShareCode(err, drive.ShareErrNotFound) {
		t.Fatalf("deleted GetShare error = %v", err)
	}
}

func TestShareParseOpenBrowseAndFetch(t *testing.T) {
	f := newFakeProvider(t)
	folder := f.addEntry(0, "shared-folder", true, 0, "", "")
	sub := f.addEntry(folder.ID, "nested", true, 0, "", "")
	file := f.addEntry(sub.ID, "file.bin", false, 12, "", "")
	f.content[idString(file.ID)] = []byte("hello drime!")
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{
		Entries:  []drive.Entry{f.entries[folder.ID].entry("")},
		Passcode: "pw",
	})
	if err != nil {
		t.Fatalf("CreateShare folder: %v", err)
	}

	link, ok := d.ParseShareLink("https://dri.me/" + share.LinkID + "?pwd=pw")
	if !ok || link.LinkID != share.LinkID || link.Passcode != "pw" {
		t.Fatalf("ParseShareLink = %+v %v", link, ok)
	}
	if _, ok := d.ParseShareLink("https://app.drime.cloud/drive/s/" + share.LinkID); !ok {
		t.Fatal("app.drime.cloud link not parsed")
	}
	if _, ok := d.ParseShareLink("https://example.com/x"); ok {
		t.Fatal("foreign link parsed")
	}
	if _, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: share.LinkID}); !isShareCode(err, drive.ShareErrPasscodeRequired) {
		t.Fatalf("missing passcode error = %v", err)
	}
	if _, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: share.LinkID, Passcode: "wrong"}); !isShareCode(err, drive.ShareErrPasscodeWrong) {
		t.Fatalf("wrong passcode error = %v", err)
	}
	session, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: share.LinkID, Passcode: "pw"})
	if err != nil {
		t.Fatalf("OpenShare: %v", err)
	}
	rootPage, err := d.ListShareEntries(context.Background(), session, drive.SharePath{}, "")
	if err != nil {
		t.Fatalf("ListShareEntries root: %v", err)
	}
	if len(rootPage.Entries) != 1 || rootPage.Entries[0].Name != "nested" {
		t.Fatalf("root entries = %+v", rootPage.Entries)
	}
	nestedPage, err := d.ListShareEntries(context.Background(), session, drive.SharePath{ID: rootPage.Entries[0].ID}, "")
	if err != nil {
		t.Fatalf("ListShareEntries nested: %v", err)
	}
	if len(nestedPage.Entries) != 1 || nestedPage.Entries[0].Name != "file.bin" {
		t.Fatalf("nested entries = %+v", nestedPage.Entries)
	}
	targets, err := d.ShareDownloadTargets(context.Background(), drive.ShareFetchRequest{
		Session: session,
		Entries: nestedPage.Entries,
	})
	if err != nil {
		t.Fatalf("ShareDownloadTargets: %v", err)
	}
	if len(targets) != 1 || !strings.Contains(targets[0].URL, "shareable_link=") || !strings.Contains(targets[0].URL, "password=pw") {
		t.Fatalf("targets = %+v", targets)
	}

	// A file link can be opened and fetched as well.
	fileShare, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{
		Entries:  []drive.Entry{f.entries[file.ID].entry(idString(sub.ID))},
		Passcode: "pw",
	})
	if err != nil {
		t.Fatalf("CreateShare file: %v", err)
	}
	fileSession, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: fileShare.LinkID, Passcode: "pw"})
	if err != nil {
		t.Fatalf("OpenShare file: %v", err)
	}
	fileEntry := f.entries[file.ID].entry("")
	fileEntry.ID = f.entries[file.ID].Hash
	fileTargets, err := d.ShareDownloadTargets(context.Background(), drive.ShareFetchRequest{
		Session: fileSession,
		Entries: []drive.ShareEntry{{Entry: fileEntry}},
	})
	if err != nil || len(fileTargets) != 1 {
		t.Fatalf("file targets = %+v, %v", fileTargets, err)
	}
}

func TestCapabilitiesDebugAndMetrics(t *testing.T) {
	f := newFakeProvider(t)
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	want := []drive.Capability{
		drive.CapabilityMtime,
		drive.CapabilityPathResolver,
		drive.CapabilityRemoteNameResolver,
		drive.CapabilityResumableUploader,
		drive.CapabilityServerSideCopy,
		drive.CapabilityShare,
		drive.CapabilityShareFetch,
		drive.CapabilityShareReceive,
		drive.CapabilityShareSave,
		drive.CapabilitySourceUploader,
		drive.CapabilitySpace,
		drive.CapabilityWriter,
	}
	got := d.Capabilities()
	if len(got) != len(want) {
		t.Fatalf("capabilities = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("capability[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	if _, err := d.List(context.Background(), ""); err != nil {
		t.Fatalf("List: %v", err)
	}
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatalf("DebugSnapshot: %v", err)
	}
	if strings.Contains(snapshotText(snapshot), f.token) {
		t.Fatal("debug snapshot leaked the token")
	}
	metrics, err := d.Metrics(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(metrics) == 0 {
		t.Fatal("no metrics recorded")
	}
	for _, metric := range metrics {
		if strings.Contains(metric.URL, f.token) || strings.Contains(metric.Error, f.token) {
			t.Fatal("metric leaked the token")
		}
	}
}

func isShareCode(err error, code drive.ShareErrorCode) bool {
	var shareErr *drive.ShareError
	return errors.As(err, &shareErr) && shareErr.Code == code
}

func snapshotText(snapshot drive.DebugSnapshot) string {
	return snapshot.Driver + " " + snapshot.Health + " " + snapshot.Stats["root_id"].(string)
}

func TestShareRejectsUnsupportedOptionsAndDownload(t *testing.T) {
	f := newFakeProvider(t)
	file := f.addEntry(0, "file.bin", false, 4, "", "")
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{
		Entries:  []drive.Entry{file.entry("")},
		Passcode: "pw",
	})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	public := true
	if _, err := d.UpdateShare(context.Background(), drive.ShareUpdateRequest{ID: share.LinkID, Public: &public}); !isShareCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("public update error = %v", err)
	}
	title := "title"
	if _, err := d.UpdateShare(context.Background(), drive.ShareUpdateRequest{ID: share.LinkID, Title: &title}); !isShareCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("title update error = %v", err)
	}
	link := f.links[share.LinkID]
	link.AllowDownload = false
	f.plainPassword[share.LinkID] = "pw"
	session, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: share.LinkID, Passcode: "pw"})
	if err != nil {
		t.Fatalf("OpenShare: %v", err)
	}
	entry := file.entry("")
	entry.ID = file.Hash
	if _, err := d.ShareDownloadTargets(context.Background(), drive.ShareFetchRequest{Session: session, Entries: []drive.ShareEntry{{Entry: entry}}}); !isShareCode(err, drive.ShareErrUnsupported) {
		t.Fatalf("download target error = %v", err)
	}
}

func TestShareFolderPagination(t *testing.T) {
	f := newFakeProvider(t)
	folder := f.addEntry(0, "many", true, 0, "", "")
	for i := 0; i < 55; i++ {
		f.addEntry(folder.ID, fmt.Sprintf("file-%02d.bin", i), false, 1, "", "")
	}
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{f.entries[folder.ID].entry("")}})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	session, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: share.LinkID})
	if err != nil {
		t.Fatalf("OpenShare: %v", err)
	}
	first, err := d.ListShareEntries(context.Background(), session, drive.SharePath{}, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Entries) != 50 || first.Next != "2" {
		t.Fatalf("first page = %d next=%q", len(first.Entries), first.Next)
	}
	second, err := d.ListShareEntries(context.Background(), session, drive.SharePath{}, first.Next)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Entries) != 5 || second.Next != "" {
		t.Fatalf("second page = %d next=%q", len(second.Entries), second.Next)
	}
}

func TestShareSave(t *testing.T) {
	f := newFakeProvider(t)
	src := f.addEntry(0, "save-me.bin", false, 7, "", "")
	dest := f.addEntry(0, "dest", true, 0, "", "")
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{f.entries[src.ID].entry("")}})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	session, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: share.LinkID})
	if err != nil {
		t.Fatalf("OpenShare: %v", err)
	}
	entry := f.entries[src.ID].entry("")
	entry.ID = f.entries[src.ID].Hash
	outcome, err := d.SaveShareEntries(context.Background(), drive.ShareSaveRequest{
		Session: session,
		Entries: []drive.ShareEntry{{Entry: entry}},
		Dest:    drive.ShareDestination{Entry: f.entries[dest.ID].entry("")},
	})
	if err != nil {
		t.Fatalf("SaveShareEntries: %v", err)
	}
	if outcome.State != drive.SaveStateSucceeded || len(outcome.Items) != 1 || outcome.Items[0].Saved == nil {
		t.Fatalf("outcome = %+v", outcome)
	}
	children, err := d.List(context.Background(), idString(dest.ID))
	if err != nil || len(children) != 1 || children[0].Name != "save-me.bin" {
		t.Fatalf("destination = %+v, %v", children, err)
	}
}

func TestShareExpiredAndRevoked(t *testing.T) {
	f := newFakeProvider(t)
	file := f.addEntry(0, "expiring.bin", false, 1, "", "")
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{file.entry("")}})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	link := f.links[share.LinkID]
	past := "2020-01-01T00:00:00.000000Z"
	link.ExpiresAt = &past
	if _, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: share.LinkID}); !isShareCode(err, drive.ShareErrExpired) {
		t.Fatalf("expired error = %v", err)
	}
	link.ExpiresAt = nil
	revoked := "2020-01-01T00:00:00.000000Z"
	link.RevokedAt = &revoked
	if _, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: share.LinkID}); !isShareCode(err, drive.ShareErrExpired) {
		t.Fatalf("revoked error = %v", err)
	}
}

func TestShareSavePartialFailure(t *testing.T) {
	f := newFakeProvider(t)
	folder := f.addEntry(0, "many", true, 0, "", "")
	f.addEntry(folder.ID, "a.bin", false, 1, "", "")
	f.addEntry(folder.ID, "b.bin", false, 1, "", "")
	dest := f.addEntry(0, "dest", true, 0, "", "")
	d := f.newDriver(t, Options{})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	share, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{f.entries[folder.ID].entry("")}})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	session, err := d.OpenShare(context.Background(), drive.ShareLink{LinkID: share.LinkID})
	if err != nil {
		t.Fatalf("OpenShare: %v", err)
	}
	page, err := d.ListShareEntries(context.Background(), session, drive.SharePath{}, "")
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("share listing = %+v, %v", page, err)
	}
	f.failImportAt = 2
	outcome, err := d.SaveShareEntries(context.Background(), drive.ShareSaveRequest{
		Session: session,
		Entries: page.Entries,
		Dest:    drive.ShareDestination{Entry: f.entries[dest.ID].entry("")},
	})
	if err != nil {
		t.Fatalf("SaveShareEntries: %v", err)
	}
	if outcome.State != drive.SaveStatePartial || len(outcome.Items) != 2 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Items[0].Saved == nil || outcome.Items[1].Error == nil {
		t.Fatalf("items = %+v", outcome.Items)
	}
}
