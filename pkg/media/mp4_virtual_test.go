package media

import (
	"bytes"
	"context"
	"encoding/binary"
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
}
