package koofr

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// virtualFixture wires a virtual-root driver against a fake account with the
// standard external mounts (writable "wgdrive", offline, read-only).
func virtualFixture(t *testing.T) (*fakeProvider, *Driver) {
	t.Helper()
	f := newFakeProvider(t)
	f.putFile("/primary.bin", []byte("primary-data"), 1700000000123)
	f.addStandardExternalMounts()
	d := f.newVirtualDriver(t, Options{})
	return f, d
}

func TestVirtualRootListAggregatesExternalMounts(t *testing.T) {
	_, d := virtualFixture(t)
	entries, err := d.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List root: %v", err)
	}
	byName := map[string]drive.Entry{}
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	// Primary content plus every online external mount, no wrapper folder.
	primary := byName["primary.bin"]
	if primary.ID != "/primary.bin" {
		t.Fatalf("primary entry = %+v", primary)
	}
	wg := byName["wgdrive"]
	if !wg.IsDir || wg.ID != "ext:ext-drive" || wg.ParentID != "/" {
		t.Fatalf("wgdrive entry = %+v", wg)
	}
	ro := byName["romount"]
	if !ro.IsDir || ro.ID != "ext:ext-ro" {
		t.Fatalf("romount entry = %+v", ro)
	}
	// Offline mounts never appear in the virtual root.
	if _, listed := byName["offline-mnt"]; listed {
		t.Fatal("offline external mount listed in the virtual root")
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 (%+v)", len(entries), entries)
	}
}

func TestVirtualRootExternalListReadAndInfoIDs(t *testing.T) {
	_, d := virtualFixture(t)
	entries, err := d.List(context.Background(), "ext:ext-drive")
	if err != nil {
		t.Fatalf("List ext root: %v", err)
	}
	if len(entries) != 2 { // existing.bin + nested
		t.Fatalf("entries = %+v", entries)
	}
	var existing drive.Entry
	for _, entry := range entries {
		if !strings.HasPrefix(entry.ID, "ext:ext-drive/") || entry.ParentID != "ext:ext-drive" {
			t.Fatalf("ext entry id = %q parent = %q", entry.ID, entry.ParentID)
		}
		if entry.Name == "existing.bin" {
			existing = entry
		}
	}
	if existing.ID != "ext:ext-drive/existing.bin" {
		t.Fatalf("existing.bin id = %q", existing.ID)
	}
	reader, err := d.Read(context.Background(), existing, 0, 0)
	if err != nil {
		t.Fatalf("Read ext file: %v", err)
	}
	data, _ := io.ReadAll(reader)
	reader.Close()
	if string(data) != "ext-data" {
		t.Fatalf("ext read = %q", data)
	}
	// Nested listings keep the prefix through every level.
	nested, err := d.List(context.Background(), "ext:ext-drive/nested")
	if err != nil {
		t.Fatalf("List nested: %v", err)
	}
	if len(nested) != 1 || nested[0].ID != "ext:ext-drive/nested/leaf.bin" {
		t.Fatalf("nested = %+v", nested)
	}
}

func TestVirtualRootExternalWritesRouteByID(t *testing.T) {
	_, d := virtualFixture(t)
	ctx := context.Background()
	dir, err := d.Mkdir(ctx, "ext:ext-drive", "work")
	if err != nil {
		t.Fatalf("Mkdir on ext mount: %v", err)
	}
	if dir.ID != "ext:ext-drive/work" || !dir.IsDir {
		t.Fatalf("mkdir entry = %+v", dir)
	}
	mod := time.UnixMilli(1700000000999).UTC()
	file, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: dir.ID,
		Name:     "uploaded.bin",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("ext-upload")),
		ModTime:  mod,
	})
	if err != nil {
		t.Fatalf("upload to ext mount: %v", err)
	}
	if file.ID != "ext:ext-drive/work/uploaded.bin" || file.ModTime.UnixMilli() != mod.UnixMilli() {
		t.Fatalf("uploaded = %+v", file)
	}
	if err := d.Remove(ctx, file); err != nil {
		t.Fatalf("Remove on ext mount: %v", err)
	}
	entries, _ := d.List(ctx, dir.ID)
	for _, entry := range entries {
		if entry.Name == "uploaded.bin" {
			t.Fatal("removed ext file still listed")
		}
	}
}

func TestVirtualRootCrossMountCopyAndMove(t *testing.T) {
	_, d := virtualFixture(t)
	ctx := context.Background()
	staging, err := d.Mkdir(ctx, "", "staging")
	if err != nil {
		t.Fatalf("Mkdir staging: %v", err)
	}
	mod := time.UnixMilli(1700000000123).UTC()
	src, err := d.PutSource(ctx, drive.UploadRequest{
		ParentID: "/",
		Name:     "cross.bin",
		Source:   drive.NewBytesReadOnlyFileSource([]byte("cross-mount")),
		ModTime:  mod,
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	// Copy primary -> external.
	into, err := d.Copy(ctx, src, "ext:ext-drive", "cross-copy.bin")
	if err != nil {
		t.Fatalf("copy to ext mount: %v", err)
	}
	if into.ID != "ext:ext-drive/cross-copy.bin" || into.Size != int64(len("cross-mount")) {
		t.Fatalf("cross copy = %+v", into)
	}
	if into.ModTime.UnixMilli() != mod.UnixMilli() {
		t.Fatalf("cross copy mtime = %v, want %v", into.ModTime, mod)
	}
	// Move external -> primary staging.
	moved, err := d.Move(ctx, into, staging.ID)
	if err != nil {
		t.Fatalf("move from ext mount: %v", err)
	}
	if moved.ID != "/staging/cross-copy.bin" {
		t.Fatalf("cross move id = %q", moved.ID)
	}
	// The source is gone from the external mount.
	extEntries, _ := d.List(ctx, "ext:ext-drive")
	for _, entry := range extEntries {
		if entry.Name == "cross-copy.bin" {
			t.Fatal("moved file still on the ext mount")
		}
	}
	// And readable from the primary side.
	reader, err := d.Read(ctx, moved, 0, 0)
	if err != nil {
		t.Fatalf("read moved: %v", err)
	}
	data, _ := io.ReadAll(reader)
	reader.Close()
	if string(data) != "cross-mount" {
		t.Fatalf("moved content = %q", data)
	}
}

func TestVirtualRootExternalRenameResetsMtime(t *testing.T) {
	_, d := virtualFixture(t)
	ctx := context.Background()
	entries, _ := d.List(ctx, "ext:ext-drive")
	var existing drive.Entry
	for _, entry := range entries {
		if entry.Name == "existing.bin" {
			existing = entry
		}
	}
	if existing.ID == "" {
		t.Fatal("existing.bin not listed")
	}
	before := existing.ModTime
	time.Sleep(10 * time.Millisecond)
	renamed, err := d.Rename(ctx, existing, "renamed.bin")
	if err != nil {
		t.Fatalf("Rename on ext mount: %v", err)
	}
	if renamed.ID != "ext:ext-drive/renamed.bin" {
		t.Fatalf("renamed id = %q", renamed.ID)
	}
	// Probe-observed provider behavior: external mounts reset the mtime on
	// rename. The driver reports the provider's answer without patching it.
	if !renamed.ModTime.After(before) {
		t.Fatalf("ext rename mtime = %v, want reset after %v", renamed.ModTime, before)
	}
	// The primary mount preserves the mtime across a rename.
	primary := drive.Entry{}
	list, _ := d.List(ctx, "/")
	for _, entry := range list {
		if entry.Name == "primary.bin" {
			primary = entry
		}
	}
	primaryMod := time.UnixMilli(1700000000123).UTC()
	if primary.ID == "" || primary.ModTime.UnixMilli() != primaryMod.UnixMilli() {
		t.Fatalf("primary fixture = %+v", primary)
	}
	primaryRenamed, err := d.Rename(ctx, primary, "primary-renamed.bin")
	if err != nil {
		t.Fatalf("Rename on primary: %v", err)
	}
	if primaryRenamed.ModTime.UnixMilli() != primaryMod.UnixMilli() {
		t.Fatalf("primary rename mtime = %v, want preserved %v", primaryRenamed.ModTime, primaryMod)
	}
}

func TestVirtualRootNameConflictFailsInit(t *testing.T) {
	f := newFakeProvider(t)
	f.mkdirAll("/wgdrive") // collides with the external mount name
	f.addStandardExternalMounts()
	opts := Options{Email: f.authEmail, Password: f.authPassword, APIBaseURL: f.server.URL}
	d := New(opts)
	d.cl.wait = func(ctx context.Context, _ int, _ time.Duration) error { return ctx.Err() }
	err := d.Init(context.Background())
	if err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Init error = %v, want invalid input", err)
	}
	if !strings.Contains(err.Error(), "wgdrive") || !strings.Contains(err.Error(), "mount_id") {
		t.Fatalf("error lacks guidance: %v", err)
	}
}

func TestVirtualRootRootPathMustBeSlash(t *testing.T) {
	f := newFakeProvider(t)
	f.mkdirAll("/docs")
	f.addStandardExternalMounts()
	opts := Options{Email: f.authEmail, Password: f.authPassword, APIBaseURL: f.server.URL, RootPath: "/docs"}
	d := New(opts)
	d.cl.wait = func(ctx context.Context, _ int, _ time.Duration) error { return ctx.Err() }
	err := d.Init(context.Background())
	if err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Init error = %v, want invalid input", err)
	}
	if !strings.Contains(err.Error(), "mount_id") {
		t.Fatalf("error lacks mount_id guidance: %v", err)
	}
}

func TestVirtualRootReadOnlyExternalRejectsWrites(t *testing.T) {
	_, d := virtualFixture(t)
	ctx := context.Background()
	entries, err := d.List(ctx, "ext:ext-ro")
	if err != nil {
		t.Fatalf("List read-only mount: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "doc.bin" {
		t.Fatalf("entries = %+v", entries)
	}
	reader, err := d.Read(ctx, entries[0], 0, 3)
	if err != nil {
		t.Fatalf("Read read-only mount: %v", err)
	}
	data, _ := io.ReadAll(reader)
	reader.Close()
	if string(data) != "rea" {
		t.Fatalf("read = %q", data)
	}
	if _, err := d.Mkdir(ctx, "ext:ext-ro", "denied"); err == nil || !errors.Is(err, drive.ErrPermission) {
		t.Fatalf("Mkdir error = %v, want permission", err)
	}
	if _, err := d.PutSource(ctx, drive.UploadRequest{ParentID: "ext:ext-ro", Name: "x.bin", Source: drive.NewBytesReadOnlyFileSource([]byte("x"))}); err == nil || !errors.Is(err, drive.ErrPermission) {
		t.Fatalf("upload error = %v, want permission", err)
	}
	if err := d.Remove(ctx, entries[0]); err == nil || !errors.Is(err, drive.ErrPermission) {
		t.Fatalf("remove error = %v, want permission", err)
	}
	if _, err := d.Rename(ctx, entries[0], "y.bin"); err == nil || !errors.Is(err, drive.ErrPermission) {
		t.Fatalf("rename error = %v, want permission", err)
	}
	if _, err := d.Copy(ctx, entries[0], "ext:ext-ro", "z.bin"); err == nil || !errors.Is(err, drive.ErrPermission) {
		t.Fatalf("copy error = %v, want permission", err)
	}
}

func TestVirtualRootOfflineAndUnknownExtIDsRejected(t *testing.T) {
	_, d := virtualFixture(t)
	ctx := context.Background()
	// The offline mount is cached nowhere: its ids are rejected.
	if _, err := d.List(ctx, "ext:ext-offline"); err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("List offline ext error = %v, want invalid input", err)
	}
	if _, err := d.List(ctx, "ext:ghost"); err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("List unknown ext error = %v, want invalid input", err)
	}
	if err := d.Remove(ctx, drive.Entry{ID: "ext:ghost/x.bin", Name: "x.bin"}); err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Remove unknown ext error = %v, want invalid input", err)
	}
}

func TestVirtualRootRemoveRefusesExternalMountRoot(t *testing.T) {
	_, d := virtualFixture(t)
	err := d.Remove(context.Background(), drive.Entry{ID: "ext:ext-drive", ParentID: "/", Name: "wgdrive", IsDir: true})
	if err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("Remove ext root error = %v, want invalid input", err)
	}
}

func TestVirtualRootResolvePathRoutesExternalMount(t *testing.T) {
	_, d := virtualFixture(t)
	ctx := context.Background()
	id, err := d.ResolvePath(ctx, "/wgdrive/nested/leaf.bin")
	if err != nil || id != "ext:ext-drive/nested/leaf.bin" {
		t.Fatalf("ResolvePath = %q, %v", id, err)
	}
	id, err = d.ResolvePath(ctx, "/WGDRIVE")
	if err != nil || id != "ext:ext-drive" {
		t.Fatalf("ResolvePath mount dir = %q, %v", id, err)
	}
	if _, err := d.ResolvePath(ctx, "/wgdrive/missing.bin"); err == nil || !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("ResolvePath missing = %v, want not found", err)
	}
	// Primary paths resolve exactly as before.
	id, err = d.ResolvePath(ctx, "/primary.bin")
	if err != nil || id != "/primary.bin" {
		t.Fatalf("ResolvePath primary = %q, %v", id, err)
	}
}

func TestVirtualRootResolveRemoteNameMatchesMountNames(t *testing.T) {
	_, d := virtualFixture(t)
	info, err := d.ResolveRemoteName(context.Background(), "WGDRIVE")
	if err != nil || info.RemoteName != "wgdrive" {
		t.Fatalf("ResolveRemoteName(WGDRIVE) = %+v, %v", info, err)
	}
	// Primary entries still resolve through the primary root listing.
	info, err = d.ResolveRemoteName(context.Background(), "PRIMARY.BIN")
	if err != nil || info.RemoteName != "primary.bin" {
		t.Fatalf("ResolveRemoteName(PRIMARY.BIN) = %+v, %v", info, err)
	}
}

func TestVirtualRootSpaceReportsPrimary(t *testing.T) {
	_, d := virtualFixture(t)
	space, err := d.Space(context.Background())
	if err != nil {
		t.Fatalf("Space: %v", err)
	}
	wantTotal := int64(1058816) << 20
	if space.Total != wantTotal {
		t.Fatalf("Space total = %d, want primary %d", space.Total, wantTotal)
	}
}

func TestVirtualRootShareOnExternalEntryRejected(t *testing.T) {
	_, d := virtualFixture(t)
	entries, _ := d.List(context.Background(), "ext:ext-drive")
	_, err := d.CreateShare(context.Background(), drive.ShareCreateRequest{Entries: []drive.Entry{entries[0]}})
	if err == nil {
		t.Fatal("CreateShare on external entry accepted")
	}
	if !strings.Contains(err.Error(), "mount_id") {
		t.Fatalf("error lacks guidance: %v", err)
	}
}

func TestMountIDLockedModeHidesExternalMounts(t *testing.T) {
	f := newFakeProvider(t)
	f.putFile("/primary.bin", []byte("x"), 1)
	f.addStandardExternalMounts()
	// Locked mode: mount_id pins one mount; no aggregation, ext ids rejected.
	d := f.newDriver(t, Options{})
	entries, err := d.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.ID, "ext:") || entry.Name == "wgdrive" {
			t.Fatalf("locked mode leaked external mount: %+v", entry)
		}
	}
	if _, err := d.List(context.Background(), "ext:ext-drive"); err == nil || !errors.Is(err, drive.ErrInvalidInput) {
		t.Fatalf("List ext id error = %v, want invalid input", err)
	}
	// root_path keeps working in locked mode.
	rooted := f.newDriver(t, Options{RootPath: "/"})
	if rooted.rootPath != "/" {
		t.Fatalf("rootPath = %q", rooted.rootPath)
	}
}

func TestVirtualRootDebugSnapshotReportsLayout(t *testing.T) {
	_, d := virtualFixture(t)
	snapshot, err := d.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatalf("DebugSnapshot: %v", err)
	}
	if snapshot.Stats["virtual_root"] != true {
		t.Fatalf("stats = %+v", snapshot.Stats)
	}
	if snapshot.Stats["external_mounts"] != 2 {
		t.Fatalf("external_mounts = %v, want 2 (offline excluded)", snapshot.Stats["external_mounts"])
	}
}

func TestVirtualRootNotSyncedExternalSurfacesProviderState(t *testing.T) {
	f := newFakeProvider(t)
	f.addExternalMount(apiMount{ID: "ext-broken", Name: "broken", Online: true, CanWrite: true, CanUpload: true})
	f.mu.Lock()
	f.notSynced["ext-broken"] = true
	f.mu.Unlock()
	f.putFile("/primary.bin", []byte("x"), 1)
	d := f.newVirtualDriver(t, Options{})
	_, err := d.List(context.Background(), "ext:ext-broken")
	if err == nil || !errors.Is(err, errMountNotSynced) {
		t.Fatalf("List error = %v, want mount-not-synced", err)
	}
}
