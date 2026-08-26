package p115open

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/OpenListTeam/115-sdk-go"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

func sha1Hex(value string) string {
	sum := sha1.Sum([]byte(value))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func TestResolvePathRootUsesConfiguredRootID(t *testing.T) {
	d := &Driver{rootID: "root-cid"}
	got, err := d.ResolvePath(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "root-cid" {
		t.Fatalf("ResolvePath root = %q, want configured root id", got)
	}
}

func TestResolveRemoteNameIsIdentity(t *testing.T) {
	d := &Driver{}
	info, err := d.ResolveRemoteName(context.Background(), "plain.txt")
	if err != nil {
		t.Fatal(err)
	}
	if info.PlainName != "plain.txt" || info.RemoteName != "plain.txt" {
		t.Fatalf("unexpected remote name info: %#v", info)
	}
}

func TestRequiredUploadHashesIncludesSHA1(t *testing.T) {
	d := &Driver{}
	got := d.RequiredUploadHashes()
	if len(got) != 1 || got[0] != drive.HashSHA1 {
		t.Fatalf("RequiredUploadHashes = %+v, want [%s]", got, drive.HashSHA1)
	}
}

func TestDebugSnapshotReportsInstantUploadCount(t *testing.T) {
	d := &Driver{}
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Extra[drive.DebugExtraInstantUploadCount]; got != int64(0) {
		t.Fatalf("instant upload count = %v, want 0", got)
	}

	d.instantUploads.Add(2)
	snapshot, err = d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Extra[drive.DebugExtraInstantUploadCount]; got != int64(2) {
		t.Fatalf("instant upload count = %v, want 2", got)
	}
}

func TestInitRequiresToken(t *testing.T) {
	drv, err := drive.New("115_open", drive.Params{})
	if err != nil {
		t.Fatalf("drive.New returned error: %v", err)
	}
	err = drv.Init(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing refresh_token") {
		t.Fatalf("Init error = %v, want missing refresh_token", err)
	}
}

func TestLoadTokenStateStateWinsWhenMatchingConfig(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON(tokenStateFile, tokenState{
		AccessToken:  "state-access",
		RefreshToken: "state-refresh",
	}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{AccessToken: "cfg-access", RefreshToken: "state-refresh"})
	driver.InstallStateStore(store)

	driver.loadTokenState()

	if driver.refreshToken != "state-refresh" {
		t.Fatalf("refreshToken = %q, want state token", driver.refreshToken)
	}
	if driver.accessToken != "cfg-access" {
		t.Fatalf("accessToken = %q, want config token preserved", driver.accessToken)
	}
	if driver.tokenSource != "state" {
		t.Fatalf("tokenSource = %q, want state", driver.tokenSource)
	}
}

func TestLoadTokenStateStateWinsWhenConfigEmpty(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON(tokenStateFile, tokenState{
		AccessToken:  "state-access",
		RefreshToken: "state-refresh",
	}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{})
	driver.InstallStateStore(store)

	driver.loadTokenState()

	if driver.refreshToken != "state-refresh" || driver.accessToken != "state-access" {
		t.Fatalf("tokens = %q/%q, want state tokens", driver.accessToken, driver.refreshToken)
	}
	if driver.tokenSource != "state" {
		t.Fatalf("tokenSource = %q, want state", driver.tokenSource)
	}
}

func TestLoadTokenStateConfigWinsOnAccountSwitch(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	// State was derived from an older config token (old account/app), and the
	// config now carries a different token: treat it as a deliberate switch.
	if err := store.SaveJSON(tokenStateFile, tokenState{
		AccessToken:        "state-access",
		RefreshToken:       "state-refresh",
		ConfigRefreshToken: "old-config-refresh",
	}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{AccessToken: "cfg-access", RefreshToken: "cfg-refresh"})
	driver.InstallStateStore(store)

	driver.loadTokenState()

	if driver.refreshToken != "cfg-refresh" {
		t.Fatalf("refreshToken = %q, want config token on account switch", driver.refreshToken)
	}
	if driver.accessToken != "cfg-access" {
		t.Fatalf("accessToken = %q, want config token", driver.accessToken)
	}
}

func TestLoadTokenStateStateWinsAfterRotation(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	// Config still carries the original token A; the state holds the rotated
	// pair B derived from A. Rotation invalidates A, so B must win.
	if err := store.SaveJSON(tokenStateFile, tokenState{
		AccessToken:        "rotated-access",
		RefreshToken:       "rotated-refresh",
		ConfigRefreshToken: "original-refresh",
	}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{RefreshToken: "original-refresh"})
	driver.InstallStateStore(store)

	driver.loadTokenState()

	if driver.refreshToken != "rotated-refresh" {
		t.Fatalf("refreshToken = %q, want rotated state token", driver.refreshToken)
	}
	if driver.accessToken != "rotated-access" {
		t.Fatalf("accessToken = %q, want rotated state access token", driver.accessToken)
	}
	if driver.tokenSource != "state" {
		t.Fatalf("tokenSource = %q, want state", driver.tokenSource)
	}
}

func TestLoadTokenStateStateWinsWithoutSourceMarker(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	// States written before the source marker was recorded are treated as
	// derived from any config token and keep winning (they are more recent).
	if err := store.SaveJSON(tokenStateFile, tokenState{
		AccessToken:  "state-access",
		RefreshToken: "state-refresh",
	}); err != nil {
		t.Fatal(err)
	}
	driver := New(Options{RefreshToken: "cfg-refresh"})
	driver.InstallStateStore(store)

	driver.loadTokenState()

	if driver.refreshToken != "state-refresh" {
		t.Fatalf("refreshToken = %q, want state token for legacy state", driver.refreshToken)
	}
}

func TestSaveTokensPersistsState(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New(Options{RefreshToken: "cfg-refresh"})
	driver.InstallStateStore(store)

	driver.saveTokens("access-1", "refresh-1", "refresh")

	var state tokenState
	if err := store.LoadJSON(tokenStateFile, &state); err != nil {
		t.Fatal(err)
	}
	if state.AccessToken != "access-1" || state.RefreshToken != "refresh-1" {
		t.Fatalf("token state = %+v, want rotated pair", state)
	}
	if state.ConfigRefreshToken != "cfg-refresh" {
		t.Fatalf("config source marker = %q, want cfg-refresh", state.ConfigRefreshToken)
	}
	if state.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero")
	}
	if driver.tokenSource != "refresh" {
		t.Fatalf("tokenSource = %q, want refresh", driver.tokenSource)
	}
}

func TestSaveTokensSkipsEmpty(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New(Options{RefreshToken: "cfg"})
	driver.InstallStateStore(store)

	driver.saveTokens("", "", "refresh")

	if err := store.LoadJSON(tokenStateFile, &tokenState{}); err == nil {
		t.Fatal("expected no state written for empty tokens")
	}
}

func TestHashAllAndPrefix(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 8<<10) // 128KiB of known content
	driver := &Driver{}

	full, err := driver.hashAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 40 {
		t.Fatalf("full sha1 length = %d, want 40", len(full))
	}

	// prefix of a larger body equals full hash of the first 128KiB.
	big := append(data, bytes.Repeat([]byte("x"), 4096)...)
	prefix, err := driver.prefixSHA1(int64(len(big)), bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	if prefix != full {
		t.Fatalf("prefix sha1 = %q, want %q", prefix, full)
	}

	// prefix of a small body equals its full hash.
	small := []byte("hello")
	smallPrefix, err := driver.prefixSHA1(int64(len(small)), bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	smallFull, err := driver.hashAll(bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	if smallPrefix != smallFull {
		t.Fatalf("small prefix = %q, want %q", smallPrefix, smallFull)
	}
}

func TestHashSignRange(t *testing.T) {
	data := []byte("abcdefghijklmnop")
	driver := &Driver{}
	got, err := driver.hashSignRange(bytes.NewReader(data), "2-5")
	if err != nil {
		t.Fatal(err)
	}
	// sha1 of "cdef"
	sum := sha1Hex("cdef")
	if got != sum {
		t.Fatalf("sign range sha1 = %q, want %q", got, sum)
	}

	if _, err := driver.hashSignRange(bytes.NewReader(data), "5-2"); err == nil {
		t.Fatal("expected error for reversed range")
	}
	if _, err := driver.hashSignRange(bytes.NewReader(data), "bad"); err == nil {
		t.Fatal("expected error for malformed sign_check")
	}
}

func TestUploadPartRanges(t *testing.T) {
	parts := p115OpenUploadPartRanges(35, 16)
	want := []p115OpenUploadPartRange{
		{Number: 1, Offset: 0, Size: 16},
		{Number: 2, Offset: 16, Size: 16},
		{Number: 3, Offset: 32, Size: 3},
	}
	if len(parts) != len(want) {
		t.Fatalf("parts len = %d, want %d", len(parts), len(want))
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Fatalf("part[%d] = %+v, want %+v", i, parts[i], want[i])
		}
	}
}

func TestCalPartSize(t *testing.T) {
	cases := []struct {
		size int64
		want int64
	}{
		{0, 20 << 20},
		{1 << 30, 20 << 20},         // 1GB
		{128 << 30, 20 << 20},       // exactly 128GB
		{(128 << 30) + 1, 27487791}, // just over 128GB
		{(256 << 30) + 1, 41231687}, // just over 256GB
		{(512 << 30) + 1, 82463373}, // just over 512GB
		{(1 << 40) + 1, 5 << 30},    // just over 1TB
	}
	for _, tc := range cases {
		if got := calPartSize(tc.size); got != tc.want {
			t.Fatalf("calPartSize(%d) = %d, want %d", tc.size, got, tc.want)
		}
	}
}

func TestEntryFromFile(t *testing.T) {
	file := sdk.GetFilesResp_File{
		Fid:  "file-id",
		Pid:  "parent-id",
		Fn:   "name.txt",
		Fc:   "1",
		FS:   1024,
		Sha1: "abc123",
		Pc:   "pick-code",
		Upt:  1700000000,
		Uet:  1700000001,
	}
	entry := entryFromFile(file)
	if entry.ID != "file-id" || entry.ParentID != "parent-id" || entry.Name != "name.txt" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.IsDir {
		t.Fatal("file should not be a dir")
	}
	if entry.Size != 1024 {
		t.Fatalf("size = %d, want 1024", entry.Size)
	}
	if entry.ModTime.Unix() != 1700000000 {
		t.Fatalf("mod time = %v, want 1700000000", entry.ModTime)
	}

	dir := entryFromFile(sdk.GetFilesResp_File{Fc: "0", Fn: "dir"})
	if !dir.IsDir {
		t.Fatal("Fc=0 should be a dir")
	}
}

func TestWrappedEntryExtraPreservesRawMetadata(t *testing.T) {
	raw := sdk.GetFilesResp_File{
		Fid:  "file-id",
		Fn:   "encrypted-name",
		Fc:   "1",
		FS:   74,
		Pc:   "pick-code",
		Sha1: "abc123",
	}
	entry := drive.Entry{
		ID:    "file-id",
		Name:  "plain.txt",
		Size:  26,
		Extra: drive.EntryExtraWrapper{RemoteName: raw.Fn, Raw: raw},
	}

	if got := rawEntrySize(entry); got != raw.FS {
		t.Fatalf("rawEntrySize = %d, want %d", got, raw.FS)
	}
	if got := entrySHA1(entry); got != "ABC123" {
		t.Fatalf("entrySHA1 = %q, want ABC123", got)
	}
}

func TestUploadSessionBindingPersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	store := drive.NewFileStateStore(filepath.Join(dir, "driver"))
	driver := New(Options{})
	driver.InstallStateStore(store)

	key := session.Identity{ParentID: "0", Name: "video.bin", Size: 32 << 20, Fingerprint: "ABC"}.Key()
	token, err := json.Marshal(p115OpenToken{Bucket: "bucket", Object: "object", UploadID: "upload-id", PartSize: 20 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.sessions.Create(key, token); err != nil {
		t.Fatal(err)
	}

	reloaded := session.NewIndex(drive.NewFileStateStore(filepath.Join(dir, "driver")), p115OpenSessionFile, session.IndexOptions{})
	binding, ok := reloaded.Get(key)
	if !ok {
		t.Fatal("expected binding to survive a new index instance")
	}
	var tok p115OpenToken
	if err := json.Unmarshal(binding.Token, &tok); err != nil {
		t.Fatal(err)
	}
	if tok.UploadID != "upload-id" || tok.Bucket != "bucket" || tok.Object != "object" {
		t.Fatalf("unexpected persisted token: %+v", tok)
	}
}
