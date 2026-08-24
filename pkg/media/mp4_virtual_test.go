package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"testing"
)

func TestMP4FastStartVirtualFileReadsVirtualLayout(t *testing.T) {
	co64 := make([]byte, 24)
	binary.BigEndian.PutUint32(co64[0:4], uint32(len(co64)))
	copy(co64[4:8], "co64")
	binary.BigEndian.PutUint32(co64[12:16], 1)
	binary.BigEndian.PutUint64(co64[16:24], 16)

	ftyp := atomBytes("ftyp", []byte("isom"))
	mdat := atomBytes("mdat", []byte("payload"))
	moov := atomBytes("moov", co64)
	raw := appendAtoms(ftyp, mdat, moov)

	vf, err := NewMP4FastStartVirtualFile(context.Background(), int64(len(raw)), bytesReadAt(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer vf.Close()

	info := vf.Info()
	if info.Mode != VirtualModeMP4FastStart || !info.Transformed || info.Size != int64(len(raw)) {
		t.Fatalf("info = %+v, want transformed mp4 with original size", info)
	}
	all, err := vf.ReadAt(context.Background(), 0, len(raw))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(raw))
	n, err := vf.ReadAtInto(context.Background(), 0, buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(raw) || !bytes.Equal(buf[:n], all) {
		t.Fatalf("ReadAtInto n=%d matches=%v, want transformed layout", n, bytes.Equal(buf[:n], all))
	}
	first, err := readAtom(context.Background(), bytesReadAt(all), 0, int64(len(all)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := readAtom(context.Background(), bytesReadAt(all), first.size, int64(len(all)))
	if err != nil {
		t.Fatal(err)
	}
	third, err := readAtom(context.Background(), bytesReadAt(all), first.size+second.size, int64(len(all)))
	if err != nil {
		t.Fatal(err)
	}
	if first.typ != "ftyp" || second.typ != "moov" || third.typ != "mdat" {
		t.Fatalf("atom order = %s,%s,%s; want ftyp,moov,mdat", first.typ, second.typ, third.typ)
	}
	if !bytes.Equal(all[len(all)-7:], []byte("payload")) {
		t.Fatalf("virtual mdat payload = %q, want payload", all[len(all)-7:])
	}
	patchedOffset := binary.BigEndian.Uint64(all[first.size+8+16 : first.size+8+24])
	if want := uint64(16 + len(moov)); patchedOffset != want {
		t.Fatalf("patched co64 offset = %d, want %d", patchedOffset, want)
	}
	mappings := vf.ReadMappings(int64(len(ftyp)-2), len(moov)+6)
	wantMappings := []VirtualReadMapping{
		{VirtualOffset: int64(len(ftyp) - 2), Length: 2, Source: "ftyp", SourceOffset: int64(len(ftyp) - 2)},
		{VirtualOffset: int64(len(ftyp)), Length: len(moov), Source: "moov", SourceOffset: int64(len(ftyp) + len(mdat))},
		{VirtualOffset: int64(len(ftyp) + len(moov)), Length: 4, Source: "mdat", SourceOffset: int64(len(ftyp))},
	}
	if !readMappingsEqual(mappings, wantMappings) {
		t.Fatalf("ReadMappings = %+v, want %+v", mappings, wantMappings)
	}
}

func TestVirtualAutoMediaFallsBackToPassthrough(t *testing.T) {
	raw := []byte("not mp4")
	vf, err := NewVirtualFile(context.Background(), VirtualModeAutoMedia, int64(len(raw)), bytesReadAt(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer vf.Close()
	if got := vf.Info().Mode; got != VirtualModePassthrough {
		t.Fatalf("mode = %q, want passthrough", got)
	}
	data, err := vf.ReadAt(context.Background(), 4, 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "mp4" {
		t.Fatalf("ReadAt = %q, want mp4", data)
	}
	buf := make([]byte, len(raw))
	n, err := vf.ReadAtInto(context.Background(), 0, buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(raw) || !bytes.Equal(buf[:n], raw) {
		t.Fatalf("ReadAtInto n=%d data=%q, want passthrough layout", n, string(buf[:n]))
	}
	mappings := vf.ReadMappings(2, 4)
	want := []VirtualReadMapping{{VirtualOffset: 2, Length: 4, Source: "passthrough", SourceOffset: 2}}
	if !readMappingsEqual(mappings, want) {
		t.Fatalf("ReadMappings = %+v, want %+v", mappings, want)
	}
}

func TestMP4FastStartVirtualFileReadsLargeMoovInChunks(t *testing.T) {
	moov := atomBytes("moov", atomBytes("free", bytes.Repeat([]byte{0}, 4<<20)))
	raw := appendAtoms(
		atomBytes("ftyp", []byte("isom")),
		atomBytes("mdat", []byte("payload")),
		moov,
	)
	readAt := func(ctx context.Context, offset int64, length int) ([]byte, error) {
		if length > 4<<20 {
			return nil, fmt.Errorf("read limit: %d", length)
		}
		return bytesReadAt(raw)(ctx, offset, length)
	}

	vf, err := NewMP4FastStartVirtualFile(context.Background(), int64(len(raw)), readAt)
	if err != nil {
		t.Fatal(err)
	}
	defer vf.Close()
	if got := vf.Info().Mode; got != VirtualModeMP4FastStart {
		t.Fatalf("mode = %q, want %q", got, VirtualModeMP4FastStart)
	}
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
