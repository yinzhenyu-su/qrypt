package core

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// --- Legacy API compatibility ---------------------------------------------

// The legacy ListPage / Capabilities / Mounts methods keep their original
// VFS-typed signatures; these interface assertions fail to compile if the
// signatures ever change. The client-facing variants return the core-owned
// contracts instead, so only client code moves onto the boundary while
// existing callers stay source-compatible.

type legacyListPageAPI interface {
	ListPage(ctx context.Context, path, cursor string, limit int) (vfs.ListPageResult, error)
}

type legacyCapabilitiesAPI interface {
	Capabilities(ctx context.Context, path string) (vfs.CapabilityInfo, error)
}

type legacyMountsAPI interface {
	Mounts() ([]vfs.MountInfo, error)
}

type clientListPageAPI interface {
	ListPageClient(ctx context.Context, path, cursor string, limit int) (ListPageResult, error)
}

type clientCapabilitiesAPI interface {
	CapabilitiesClient(ctx context.Context, path string) (CapabilityInfo, error)
}

type clientMountsAPI interface {
	MountsClient() ([]MountInfo, error)
}

var (
	_ legacyListPageAPI     = (*Core)(nil)
	_ legacyCapabilitiesAPI = (*Core)(nil)
	_ legacyMountsAPI       = (*Core)(nil)
	_ clientListPageAPI     = (*Core)(nil)
	_ clientCapabilitiesAPI = (*Core)(nil)
	_ clientMountsAPI       = (*Core)(nil)
)

// TestClientMethodsConvertFromLegacyThroughCore proves the *Client methods
// are thin conversion layers over the legacy VFS-typed APIs: for the same
// call the client result equals the conversion of the legacy result, and
// the JSON wire format is byte-identical.
func TestClientMethodsConvertFromLegacyThroughCore(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := drive.NewFakeDriver()
	if err := raw.Seed(map[string]string{"a.txt": "alpha", "b.txt": "beta"}); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(raw, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	legacyPage, err := c.ListPage(ctx, "/", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	clientPage, err := c.ListPageClient(ctx, "/", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(clientPage, listPageResultFromVFS(legacyPage)) {
		t.Fatalf("ListPageClient = %+v, want conversion of legacy %+v", clientPage, legacyPage)
	}
	if len(clientPage.Entries) != 2 {
		t.Fatalf("ListPageClient entries = %d, want 2", len(clientPage.Entries))
	}
	assertSameJSON(t, "ListPageClient", []ListPageResult{clientPage}, []vfs.ListPageResult{legacyPage})

	legacyCaps, err := c.Capabilities(ctx, "/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	clientCaps, err := c.CapabilitiesClient(ctx, "/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(clientCaps, capabilityInfoFromVFS(legacyCaps)) {
		t.Fatalf("CapabilitiesClient = %+v, want conversion of legacy %+v", clientCaps, legacyCaps)
	}
	assertSameJSON(t, "CapabilitiesClient", clientCaps, legacyCaps)

	legacyMounts, err := c.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	clientMounts, err := c.MountsClient()
	if err != nil {
		t.Fatal(err)
	}
	if len(clientMounts) != len(legacyMounts) {
		t.Fatalf("MountsClient len = %d, want %d", len(clientMounts), len(legacyMounts))
	}
	for i := range legacyMounts {
		if clientMounts[i] != mountInfoFromVFS(legacyMounts[i]) {
			t.Fatalf("MountsClient[%d] = %+v, want conversion of legacy %+v", i, clientMounts[i], legacyMounts[i])
		}
	}
	assertSameJSON(t, "MountsClient", clientMounts, legacyMounts)
}

// TestClientMethodsPreserveLegacyErrors proves error paths (closed core,
// unsupported surfaces) pass through the client layer unchanged.
func TestClientMethodsPreserveLegacyErrors(t *testing.T) {
	t.Parallel()
	closed := &Core{}
	if _, err := closed.ListPageClient(context.Background(), "/", "", 0); err == nil {
		t.Fatal("ListPageClient on a closed core returned nil error")
	}
	if _, err := closed.CapabilitiesClient(context.Background(), "/"); err == nil {
		t.Fatal("CapabilitiesClient on a closed core returned nil error")
	}
	if _, err := closed.MountsClient(); err == nil {
		t.Fatal("MountsClient on a closed core returned nil error")
	}
}

// --- Client DTO conversion and schema preservation ------------------------

// The client value types are independent core contracts that mirror the
// VFS definitions field for field. These tests lock both directions: every
// field survives conversion, and the JSON encoding is byte-identical, so
// the mobile JSON schema cannot drift when the VFS structs evolve.

func TestCapabilityInfoConversionPreservesFieldsAndJSON(t *testing.T) {
	t.Parallel()
	src := vfs.CapabilityInfo{
		Mount:          "q",
		Path:           "/q/docs",
		Root:           true,
		MountRoot:      false,
		Capabilities:   []drive.Capability{drive.CapabilityWriter, drive.CapabilitySpace},
		CanRead:        true,
		CanList:        true,
		CanUpload:      true,
		CanMkdir:       true,
		CanRename:      true,
		CanMove:        true,
		CanRemove:      true,
		CanSpace:       true,
		CanUploadChild: true,
		CanMkdirChild:  true,
		CanRenameChild: true,
		CanMoveChild:   true,
		CanRemoveChild: true,
	}
	got := capabilityInfoFromVFS(src)
	want := CapabilityInfo{
		Mount:          "q",
		Path:           "/q/docs",
		Root:           true,
		MountRoot:      false,
		Capabilities:   []drive.Capability{drive.CapabilityWriter, drive.CapabilitySpace},
		CanRead:        true,
		CanList:        true,
		CanUpload:      true,
		CanMkdir:       true,
		CanRename:      true,
		CanMove:        true,
		CanRemove:      true,
		CanSpace:       true,
		CanUploadChild: true,
		CanMkdirChild:  true,
		CanRenameChild: true,
		CanMoveChild:   true,
		CanRemoveChild: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CapabilityInfo conversion lost fields:\n got %+v\nwant %+v", got, want)
	}
	assertSameJSON(t, "CapabilityInfo", got, src)
}

func TestMountInfoConversionPreservesFieldsAndJSON(t *testing.T) {
	t.Parallel()
	src := vfs.MountInfo{Name: "q", Path: "/q", Encrypted: true, State: "failed", Error: "boom"}
	got := mountInfoFromVFS(src)
	want := MountInfo{Name: "q", Path: "/q", Encrypted: true, State: "failed", Error: "boom"}
	if got != want {
		t.Fatalf("MountInfo conversion lost fields: got %+v want %+v", got, want)
	}
	assertSameJSON(t, "MountInfo", got, src)

	// Omitted empty fields must stay omitted on both sides.
	b, err := json.Marshal(MountInfo{Name: "q", Path: "/q"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "\"state\"") || strings.Contains(string(b), "\"error\"") {
		t.Fatalf("empty State/Error must be omitted, got %s", b)
	}
}

func TestListPageResultConversionPreservesFieldsAndJSON(t *testing.T) {
	t.Parallel()
	entries := []drive.Entry{
		{Name: "a.txt", ID: "1", IsDir: false, Size: 3},
		{Name: "b.txt", ID: "2", IsDir: false, Size: 4},
	}
	src := vfs.ListPageResult{Entries: entries, NextCursor: "b.txt"}
	got := listPageResultFromVFS(src)
	want := ListPageResult{Entries: entries, NextCursor: "b.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListPageResult conversion lost fields: got %+v want %+v", got, want)
	}
	assertSameJSON(t, "ListPageResult", got, src)
}

func assertSameJSON(t *testing.T, name string, client, vfsValue any) {
	t.Helper()
	clientJSON, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("%s: marshal client type: %v", name, err)
	}
	vfsJSON, err := json.Marshal(vfsValue)
	if err != nil {
		t.Fatalf("%s: marshal vfs type: %v", name, err)
	}
	if !bytes.Equal(clientJSON, vfsJSON) {
		t.Fatalf("%s JSON diverged:\n client: %s\n vfs:    %s", name, clientJSON, vfsJSON)
	}
}
