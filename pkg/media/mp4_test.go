package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"testing"
)

func TestProbeMP4DetectsFastStart(t *testing.T) {
	data := appendAtoms(
		atomBytes("ftyp", []byte("isom")),
		atomBytes("moov", nil),
		atomBytes("mdat", []byte("data")),
	)
	probe, err := ProbeMP4(context.Background(), int64(len(data)), bytesReadAt(data))
	if err != nil {
		t.Fatal(err)
	}
	if !probe.IsMP4 || !probe.FastStart || probe.NeedsFastStart {
		t.Fatalf("probe = %+v, want faststart mp4", probe)
	}
}

func TestProbeMP4DetectsTailMoov(t *testing.T) {
	data := appendAtoms(
		atomBytes("ftyp", []byte("isom")),
		atomBytes("mdat", []byte("data")),
		atomBytes("moov", nil),
	)
	probe, err := ProbeMP4(context.Background(), int64(len(data)), bytesReadAt(data))
	if err != nil {
		t.Fatal(err)
	}
	if !probe.IsMP4 || probe.FastStart || !probe.NeedsFastStart {
		t.Fatalf("probe = %+v, want tail moov mp4", probe)
	}
}

func TestMP4FastStartVirtualFilePatchesCO64(t *testing.T) {
	co64 := make([]byte, 24)
	binary.BigEndian.PutUint32(co64[0:4], uint32(len(co64)))
	copy(co64[4:8], "co64")
	binary.BigEndian.PutUint32(co64[12:16], 1)
	binary.BigEndian.PutUint64(co64[16:24], 16)
	moov := atomBytes("moov", co64)
	data := appendAtoms(
		atomBytes("ftyp", []byte("isom")),
		atomBytes("mdat", []byte("payload")),
		moov,
	)

	vf, err := NewMP4FastStartVirtualFile(context.Background(), int64(len(data)), bytesReadAt(data))
	if err != nil {
		t.Fatal(err)
	}
	info := vf.Info()
	if !info.Transformed || info.MP4 == nil || !info.MP4.NeedsFastStart {
		t.Fatalf("info = %+v, want transformed tail moov mp4", info)
	}
	got, err := vf.ReadAt(context.Background(), 0, int(info.Size))
	if err != nil {
		t.Fatal(err)
	}
	first, err := readAtom(context.Background(), bytesReadAt(got), 0, int64(len(got)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := readAtom(context.Background(), bytesReadAt(got), first.size, int64(len(got)))
	if err != nil {
		t.Fatal(err)
	}
	if first.typ != "ftyp" || second.typ != "moov" {
		t.Fatalf("atom order = %s,%s; want ftyp,moov", first.typ, second.typ)
	}
	patchedOffset := binary.BigEndian.Uint64(got[first.size+8+16 : first.size+8+24])
	if want := uint64(16 + len(moov)); patchedOffset != want {
		t.Fatalf("patched co64 offset = %d, want %d", patchedOffset, want)
	}
}

func bytesReadAt(data []byte) ReadAtFunc {
	return func(ctx context.Context, offset int64, length int) ([]byte, error) {
		if offset >= int64(len(data)) {
			return []byte{}, io.EOF
		}
		end := offset + int64(length)
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		return data[offset:end], nil
	}
}

func appendAtoms(atoms ...[]byte) []byte {
	return bytes.Join(atoms, nil)
}

func atomBytes(typ string, payload []byte) []byte {
	data := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(data[0:4], uint32(len(data)))
	copy(data[4:8], typ)
	copy(data[8:], payload)
	return data
}
