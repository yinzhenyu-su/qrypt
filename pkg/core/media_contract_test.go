package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/media"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// Compile-time compatibility: the legacy media-typed Core methods keep their
// exact exported signatures so existing callers continue to compile. The
// client-facing variants exposed to pkg/mobile are pinned alongside them.
var (
	_ func(*Core, context.Context, string) (media.MP4Probe, error)             = (*Core).ProbeMP4
	_ func(*Core, context.Context, string, string) (media.VirtualFile, error)  = (*Core).OpenVirtualFile
	_ func(*Core, context.Context, string) (MP4Probe, error)                   = (*Core).ProbeMP4Client
	_ func(*Core, context.Context, string, string) (*VirtualFileHandle, error) = (*Core).OpenVirtualFileClient
)

func TestCoreMediaDTONotAliased(t *testing.T) {
	t.Parallel()
	cases := []struct {
		core, media reflect.Type
	}{
		{reflect.TypeOf(MP4Probe{}), reflect.TypeOf(media.MP4Probe{})},
		{reflect.TypeOf(VirtualFileInfo{}), reflect.TypeOf(media.VirtualFileInfo{})},
		{reflect.TypeOf(VirtualReadMapping{}), reflect.TypeOf(media.VirtualReadMapping{})},
	}
	for _, tc := range cases {
		if tc.core == tc.media {
			t.Fatalf("%v must not alias %v: core DTOs are independent contracts", tc.core, tc.media)
		}
	}
	// The core probe must be produced by core's own package, not pkg/media.
	if got := reflect.TypeOf(MP4Probe{}).PkgPath(); got != "github.com/yinzhenyu/qrypt/pkg/core" {
		t.Fatalf("core.MP4Probe pkg path = %q, want pkg/core", got)
	}
	if got := reflect.TypeOf(VirtualFileInfo{}).PkgPath(); got != "github.com/yinzhenyu/qrypt/pkg/core" {
		t.Fatalf("core.VirtualFileInfo pkg path = %q, want pkg/core", got)
	}
	if got := reflect.TypeOf(VirtualReadMapping{}).PkgPath(); got != "github.com/yinzhenyu/qrypt/pkg/core" {
		t.Fatalf("core.VirtualReadMapping pkg path = %q, want pkg/core", got)
	}
}

// requireSameJSONSchema pins field names and json tags so an accidental field
// rename or tag change in either direction is caught.
func requireSameJSONSchema(t *testing.T, coreType, mediaType reflect.Type) {
	t.Helper()
	if coreType.NumField() != mediaType.NumField() {
		t.Fatalf("field count mismatch: %v has %d fields, %v has %d", coreType, coreType.NumField(), mediaType, mediaType.NumField())
	}
	for i := 0; i < coreType.NumField(); i++ {
		cf, mf := coreType.Field(i), mediaType.Field(i)
		if cf.Name != mf.Name {
			t.Fatalf("field %d name mismatch: core %q vs media %q", i, cf.Name, mf.Name)
		}
		if cf.Tag.Get("json") != mf.Tag.Get("json") {
			t.Fatalf("json tag mismatch on %v.%s: %q vs media %q", coreType, cf.Name, cf.Tag.Get("json"), mf.Tag.Get("json"))
		}
	}
}

func TestCoreMediaDTOSchemaMatchesMedia(t *testing.T) {
	t.Parallel()
	requireSameJSONSchema(t, reflect.TypeOf(MP4Probe{}), reflect.TypeOf(media.MP4Probe{}))
	requireSameJSONSchema(t, reflect.TypeOf(VirtualFileInfo{}), reflect.TypeOf(media.VirtualFileInfo{}))
	requireSameJSONSchema(t, reflect.TypeOf(VirtualReadMapping{}), reflect.TypeOf(media.VirtualReadMapping{}))
}

func TestConvertMP4ProbeJSONEquivalent(t *testing.T) {
	t.Parallel()
	probes := []media.MP4Probe{
		{}, // all-zero: exercises omitempty on every field
		{IsMP4: true},
		{IsMP4: true, FastStart: true, NeedsFastStart: false},
		{IsMP4: false, FastStart: false, NeedsFastStart: true},
		{
			IsMP4:          true,
			FastStart:      true,
			NeedsFastStart: false,
			FtypOffset:     0,
			FtypSize:       12,
			MoovOffset:     27,
			MoovSize:       32,
			MdatOffset:     12,
			MdatSize:       15,
		},
	}
	for i, probe := range probes {
		coreJSON, err := json.Marshal(convertMP4Probe(probe))
		if err != nil {
			t.Fatal(err)
		}
		mediaJSON, err := json.Marshal(probe)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(coreJSON, mediaJSON) {
			t.Fatalf("case %d: core JSON %s != media JSON %s", i, coreJSON, mediaJSON)
		}
	}
}

func TestConvertVirtualInfoAndMappingsJSONEquivalent(t *testing.T) {
	t.Parallel()
	probe := media.MP4Probe{
		IsMP4:          true,
		NeedsFastStart: true,
		FtypSize:       12,
		MoovOffset:     27,
		MoovSize:       32,
		MdatOffset:     12,
		MdatSize:       15,
	}
	infos := []media.VirtualFileInfo{
		{Mode: media.VirtualModePassthrough, Size: 17, Transformed: false},
		{Mode: media.VirtualModeMP4FastStart, Size: 59, Transformed: true, MP4: &probe},
	}
	for i, info := range infos {
		coreJSON, err := json.Marshal(convertVirtualFileInfo(info))
		if err != nil {
			t.Fatal(err)
		}
		mediaJSON, err := json.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(coreJSON, mediaJSON) {
			t.Fatalf("case %d: core JSON %s != media JSON %s", i, coreJSON, mediaJSON)
		}
	}

	mappings := []media.VirtualReadMapping{
		{VirtualOffset: 0, Length: 12, Source: "ftyp", SourceOffset: 0},
		{VirtualOffset: 12, Length: 32, Source: "moov", SourceOffset: 27},
		{VirtualOffset: 44, Length: 15, Source: "mdat", SourceOffset: 12},
	}
	for i, src := range [][]media.VirtualReadMapping{nil, {}, mappings} {
		coreJSON, err := json.Marshal(convertReadMappings(src))
		if err != nil {
			t.Fatal(err)
		}
		mediaJSON, err := json.Marshal(src)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(coreJSON, mediaJSON) {
			t.Fatalf("case %d: core JSON %s != media JSON %s", i, coreJSON, mediaJSON)
		}
	}
}

// newMediaTestCore starts a Core over a localfs-backed VFS rooted at remote.
func newMediaTestCore(t *testing.T, remote string) *Core {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopTestVFS(t, fs) })
	fs.Start(ctx)
	return newTestCore(t, fs)
}

func TestVirtualFileHandlePassthroughLifecycle(t *testing.T) {
	t.Parallel()
	remote := t.TempDir()
	content := []byte("virtual video data")
	if err := os.WriteFile(filepath.Join(remote, "video.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	c := newMediaTestCore(t, remote)

	h, err := c.OpenVirtualFileClient(context.Background(), "/video.bin", media.VirtualModePassthrough)
	if err != nil {
		t.Fatal(err)
	}
	info := h.Info()
	if info.Mode != media.VirtualModePassthrough || info.Transformed || info.MP4 != nil || info.Size != int64(len(content)) {
		t.Fatalf("Info = %+v, want passthrough size %d", info, len(content))
	}

	buf := make([]byte, len(content))
	n, err := h.ReadAtInto(context.Background(), 0, buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(content) || !bytes.Equal(buf[:n], content) {
		t.Fatalf("ReadAtInto n=%d data=%q, want %q", n, buf[:n], content)
	}

	mappings := h.ReadMappings(8, len(content)+10) // requested past EOF, clamped to file end
	want := []VirtualReadMapping{{VirtualOffset: 8, Length: len(content) - 8, Source: "passthrough", SourceOffset: 8}}
	if len(mappings) != 1 || mappings[0] != want[0] {
		t.Fatalf("ReadMappings = %+v, want %+v", mappings, want)
	}
	if got := h.ReadMappings(-1, 4); got != nil {
		t.Fatalf("ReadMappings(-1,4) = %+v, want nil", got)
	}

	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("double Close: %v", err)
	}
	// Media passthrough Close is a no-op; the handle stays usable without
	// panicking on any operation mobile may still issue.
	if _, err := h.ReadAtInto(context.Background(), 0, buf); err != nil {
		t.Fatalf("ReadAtInto after Close: %v", err)
	}
}

func TestVirtualFileHandleAutoMP4FastStartLayout(t *testing.T) {
	t.Parallel()
	remote := t.TempDir()
	raw := rawMP4WithMoovAfterMdat(t)
	if err := os.WriteFile(filepath.Join(remote, "movie.mp4"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	c := newMediaTestCore(t, remote)

	h, err := c.OpenVirtualFileClient(context.Background(), "/movie.mp4", media.VirtualModeAutoMedia)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	info := h.Info()
	if info.Mode != media.VirtualModeMP4FastStart || !info.Transformed || info.Size != int64(len(raw)) {
		t.Fatalf("Info = %+v, want transformed mp4 faststart of size %d", info, len(raw))
	}
	if info.MP4 == nil || !info.MP4.NeedsFastStart || info.MP4.FastStart {
		t.Fatalf("Info.MP4 = %+v, want needs_fast_start", info.MP4)
	}

	// Virtual bytes: ftyp, moov (patched), mdat - the transformed order.
	buf := make([]byte, len(raw))
	n, err := h.ReadAtInto(context.Background(), 0, buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(buf) {
		t.Fatalf("ReadAtInto n=%d, want %d", n, len(buf))
	}
	first, err := parseVirtualAtom(buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseVirtualAtom(buf, first.size)
	if err != nil {
		t.Fatal(err)
	}
	third, err := parseVirtualAtom(buf, first.size+second.size)
	if err != nil {
		t.Fatal(err)
	}
	if first.typ != "ftyp" || second.typ != "moov" || third.typ != "mdat" {
		t.Fatalf("virtual atom order = %s,%s,%s; want ftyp,moov,mdat", first.typ, second.typ, third.typ)
	}
	if want := "payload"; !bytes.Equal(buf[len(buf)-len(want):], []byte(want)) {
		t.Fatalf("virtual mdat tail = %q, want %q", buf[len(buf)-len(want):], want)
	}

	mappings := h.ReadMappings(0, len(raw))
	want := []VirtualReadMapping{
		{VirtualOffset: 0, Length: 12, Source: "ftyp", SourceOffset: 0},
		{VirtualOffset: 12, Length: 32, Source: "moov", SourceOffset: 27},
		{VirtualOffset: 44, Length: 15, Source: "mdat", SourceOffset: 12},
	}
	if !readMappingsEqual(mappings, want) {
		t.Fatalf("ReadMappings = %+v, want %+v", mappings, want)
	}

	// The core-owned Info must render exactly like the legacy media path.
	legacy, err := c.OpenVirtualFile(context.Background(), "/movie.mp4", media.VirtualModeAutoMedia)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	coreJSON, err := json.Marshal(h.Info())
	if err != nil {
		t.Fatal(err)
	}
	mediaJSON, err := json.Marshal(legacy.Info())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(coreJSON, mediaJSON) {
		t.Fatalf("handle Info JSON %s != legacy media Info JSON %s", coreJSON, mediaJSON)
	}
}

func TestProbeMP4ClientMatchesLegacyProbe(t *testing.T) {
	t.Parallel()
	remote := t.TempDir()
	raw := rawMP4WithMoovAfterMdat(t)
	if err := os.WriteFile(filepath.Join(remote, "movie.mp4"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	c := newMediaTestCore(t, remote)
	ctx := context.Background()

	legacy, err := c.ProbeMP4(ctx, "/movie.mp4")
	if err != nil {
		t.Fatal(err)
	}
	info, err := c.ProbeMP4Client(ctx, "/movie.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if info != convertMP4Probe(legacy) {
		t.Fatalf("ProbeMP4Client = %+v, want converted legacy probe %+v", info, legacy)
	}
	if legacy.IsMP4 != info.IsMP4 || legacy.NeedsFastStart != info.NeedsFastStart || legacy.FastStart != info.FastStart {
		t.Fatalf("probe flags mismatch: legacy %+v vs info %+v", legacy, info)
	}
	if legacy.FtypSize != info.FtypSize || legacy.MoovOffset != info.MoovOffset || legacy.MoovSize != info.MoovSize ||
		legacy.MdatOffset != info.MdatOffset || legacy.MdatSize != info.MdatSize {
		t.Fatalf("probe offsets mismatch: legacy %+v vs info %+v", legacy, info)
	}

	coreJSON, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(coreJSON, legacyJSON) {
		t.Fatalf("ProbeMP4Client JSON %s != legacy ProbeMP4 JSON %s", coreJSON, legacyJSON)
	}
}

func TestOpenVirtualFileClientErrorsOnDirectory(t *testing.T) {
	t.Parallel()
	remote := t.TempDir()
	if err := os.MkdirAll(filepath.Join(remote, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newMediaTestCore(t, remote)
	if _, err := c.OpenVirtualFileClient(context.Background(), "/dir", media.VirtualModePassthrough); err == nil {
		t.Fatal("expected error opening a directory as a virtual file")
	}
	if _, err := c.ProbeMP4Client(context.Background(), "/dir"); err == nil {
		t.Fatal("expected error probing a directory")
	}
}

// rawMP4WithMoovAfterMdat builds ftyp+mdat+moov where moov carries a co64
// chunk table, which the faststart transform moves ahead of mdat.
func rawMP4WithMoovAfterMdat(t *testing.T) []byte {
	t.Helper()
	co64 := make([]byte, 24)
	binary.BigEndian.PutUint32(co64[0:4], uint32(len(co64)))
	copy(co64[4:8], "co64")
	binary.BigEndian.PutUint32(co64[12:16], 1)
	binary.BigEndian.PutUint64(co64[16:24], 16)
	ftyp := mp4AtomBytes("ftyp", []byte("isom"))
	mdat := mp4AtomBytes("mdat", []byte("payload"))
	moov := mp4AtomBytes("moov", co64)
	return append(ftyp, append(mdat, moov...)...)
}

func mp4AtomBytes(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// parseVirtualAtom reads an MP4 atom header (8-byte size+type) at offset.
type virtualAtom struct {
	typ  string
	size int64
}

func parseVirtualAtom(buf []byte, offset int64) (virtualAtom, error) {
	i := int(offset)
	if i+8 > len(buf) {
		return virtualAtom{}, bytes.ErrTooLarge
	}
	return virtualAtom{typ: string(buf[i+4 : i+8]), size: int64(binary.BigEndian.Uint32(buf[i : i+4]))}, nil
}

func readMappingsEqual(a, b []VirtualReadMapping) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
