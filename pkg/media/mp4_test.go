package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
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

func TestMP4FastStartConvertsOverflowingSTCOToCO64(t *testing.T) {
	stco := make([]byte, 20)
	binary.BigEndian.PutUint32(stco[0:4], uint32(len(stco)))
	copy(stco[4:8], "stco")
	binary.BigEndian.PutUint32(stco[12:16], 1)
	binary.BigEndian.PutUint32(stco[16:20], ^uint32(0)-1)
	moov := atomBytes("moov", stco)
	data := appendAtoms(
		atomBytes("ftyp", []byte("isom")),
		atomBytes("mdat", []byte("payload")),
		moov,
	)

	vf, err := NewMP4FastStartVirtualFile(context.Background(), int64(len(data)), bytesReadAt(data))
	if err != nil {
		t.Fatal(err)
	}
	defer vf.Close()
	if got, want := vf.Info().Size, int64(len(data)+4); got != want {
		t.Fatalf("virtual size = %d, want %d after stco to co64 growth", got, want)
	}
	got, err := vf.ReadAt(context.Background(), 0, int(vf.Info().Size))
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
	converted, err := readAtom(context.Background(), bytesReadAt(got), first.size+atomHeaderSize, int64(len(got)))
	if err != nil {
		t.Fatal(err)
	}
	if second.typ != "moov" || converted.typ != "co64" {
		t.Fatalf("atom order = %s,%s; want moov containing co64", second.typ, converted.typ)
	}
	third, err := readAtom(context.Background(), bytesReadAt(got), first.size+second.size, int64(len(got)))
	if err != nil {
		t.Fatal(err)
	}
	if third.typ != "mdat" || !bytes.Equal(got[len(got)-7:], []byte("payload")) {
		t.Fatalf("mdat = %s payload=%q, want payload", third.typ, got[len(got)-7:])
	}
	patchedOffset := binary.BigEndian.Uint64(got[first.size+atomHeaderSize+16 : first.size+atomHeaderSize+24])
	if want := uint64(^uint32(0)-1) + uint64(second.size); patchedOffset != want {
		t.Fatalf("converted co64 offset = %d, want %d", patchedOffset, want)
	}
}

func TestMP4FastStartDoesNotMutateReadBuffer(t *testing.T) {
	co64 := make([]byte, 24)
	binary.BigEndian.PutUint32(co64[0:4], uint32(len(co64)))
	copy(co64[4:8], "co64")
	binary.BigEndian.PutUint32(co64[12:16], 1)
	binary.BigEndian.PutUint64(co64[16:24], 16)
	data := appendAtoms(
		atomBytes("ftyp", []byte("isom")),
		atomBytes("mdat", []byte("payload")),
		atomBytes("moov", co64),
	)
	original := append([]byte(nil), data...)

	first, err := NewMP4FastStartVirtualFile(context.Background(), int64(len(data)), bytesReadAt(data))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewMP4FastStartVirtualFile(context.Background(), int64(len(data)), bytesReadAt(data))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	if !bytes.Equal(data, original) {
		t.Fatal("opening virtual file mutated ReadAtFunc buffer")
	}
	firstData, err := first.ReadAt(context.Background(), 0, int(first.Info().Size))
	if err != nil {
		t.Fatal(err)
	}
	secondData, err := second.ReadAt(context.Background(), 0, int(second.Info().Size))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstData, secondData) {
		t.Fatal("repeated opens produced different virtual files")
	}
}

func bytesReadAt(data []byte) ReadAtFunc {
	return func(ctx context.Context, offset int64, length int) ([]byte, error) {
		if offset >= int64(len(data)) {
			return []byte{}, io.EOF
		}
		end := min(offset+int64(length), int64(len(data)))
		return data[offset:end], nil
	}
}

func nestedAtom(typ string, children ...[]byte) []byte {
	return atomBytes(typ, bytes.Join(children, nil))
}

func stcoAtom(entries ...uint32) []byte {
	data := make([]byte, 16+4*len(entries))
	binary.BigEndian.PutUint32(data[0:4], uint32(len(data)))
	copy(data[4:8], "stco")
	binary.BigEndian.PutUint32(data[12:16], uint32(len(entries)))
	for i, v := range entries {
		binary.BigEndian.PutUint32(data[16+4*i:20+4*i], v)
	}
	return data
}

func TestPatchChunkOffsetsResolvesSecondOrderConversion(t *testing.T) {
	const m0 = 64 // moov = 8 + trak(36) + stcoB(20)

	va := ^uint32(0) - m0     // fits under the initial delta, overflows once grown
	vb := ^uint32(0) - m0 + 1 // already overflows the initial delta
	stcoA := stcoAtom(va)
	stcoB := stcoAtom(vb)
	moov := nestedAtom("moov",
		nestedAtom("trak", nestedAtom("stbl", stcoA)),
		stcoB,
	)
	if len(moov) != m0 {
		t.Fatalf("fixture moov size = %d, want %d", len(moov), m0)
	}

	patched, err := patchChunkOffsets(moov, int64(m0))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(patched), m0+8; got != want {
		t.Fatalf("patched size = %d, want %d (both tables converted)", got, want)
	}
	top, err := readAtom(context.Background(), bytesReadAt(patched), 0, int64(len(patched)))
	if err != nil || string(top.typ[:]) != "moov" {
		t.Fatalf("top atom = %v err=%v, want moov", top, err)
	}

	trak, err := readAtom(context.Background(), bytesReadAt(patched), 8, int64(len(patched)))
	if err != nil || string(trak.typ[:]) != "trak" {
		t.Fatalf("trak = %v err=%v", trak, err)
	}
	stbl, err := readAtom(context.Background(), bytesReadAt(patched), 8+trak.headerSize, int64(len(patched)))
	if err != nil || string(stbl.typ[:]) != "stbl" {
		t.Fatalf("stbl = %v err=%v", stbl, err)
	}
	grownA, err := readAtom(context.Background(), bytesReadAt(patched), 8+trak.headerSize+stbl.headerSize, int64(len(patched)))
	if err != nil || string(grownA.typ[:]) != "co64" {
		t.Fatalf("converted stcoA = %v err=%v, want co64", grownA, err)
	}
	grownB, err := readAtom(context.Background(), bytesReadAt(patched), 8+trak.size, int64(len(patched)))
	if err != nil || string(grownB.typ[:]) != "co64" {
		t.Fatalf("converted stcoB = %v err=%v, want co64", grownB, err)
	}

	wantA := uint64(va) + m0 + 8
	wantB := uint64(vb) + m0 + 8
	gotA := binary.BigEndian.Uint64(patched[8+trak.headerSize+stbl.headerSize+16:])
	gotB := binary.BigEndian.Uint64(patched[8+trak.size+16:])
	if gotA != wantA || gotB != wantB {
		t.Fatalf("patched offsets = %d,%d; want %d,%d", gotA, gotB, wantA, wantB)
	}
}

func TestPatchChunkOffsetsKeepsUnrelatedAtomsIntact(t *testing.T) {
	free := atomBytes("free", []byte("keepme"))
	hdlr := atomBytes("hdlr", []byte("hand"))
	meta := append([]byte{0, 0, 0, 0}, hdlr...)
	metaAtom := atomBytes("meta", meta)
	stco := stcoAtom(100, 200)
	moov := nestedAtom("moov",
		free,
		nestedAtom("trak", nestedAtom("mdia", nestedAtom("minf", nestedAtom("stbl", stco)))),
		nestedAtom("udta", metaAtom),
	)

	patched, err := patchChunkOffsets(moov, int64(len(moov)))
	if err != nil {
		t.Fatal(err)
	}
	if len(patched) != len(moov) {
		t.Fatalf("patched size = %d, want %d without conversion", len(patched), len(moov))
	}
	idx := bytes.Index(patched, []byte("keepme"))
	if idx < 0 || !bytes.Equal(patched[idx:idx+6], []byte("keepme")) {
		t.Fatalf("free payload corrupted: %q", patched[max(idx-8, 0):idx+8])
	}
	const stcoFirstEntryOffset = 16 // size+type+verFlags+count
	entryPos := bytes.Index(patched, []byte("stco")) - 4 + stcoFirstEntryOffset
	if got := binary.BigEndian.Uint32(patched[entryPos : entryPos+4]); got != uint32(100+len(moov)) {
		t.Fatalf("first chunk offset = %d, want %d", got, 100+len(moov))
	}
	if got := binary.BigEndian.Uint32(patched[entryPos+4 : entryPos+8]); got != uint32(200+len(moov)) {
		t.Fatalf("second chunk offset = %d, want %d", got, 200+len(moov))
	}
	if !bytes.Contains(patched, []byte("hdlr")) || !bytes.Contains(patched, []byte("udta")) {
		t.Fatal("meta/udta structure lost")
	}
}

func TestPatchChunkOffsetsPreservesExtendedHeaders(t *testing.T) {
	stco := extendedAtomBytes("stco", func() []byte {
		payload := make([]byte, 12)
		binary.BigEndian.PutUint32(payload[4:8], 1)
		binary.BigEndian.PutUint32(payload[8:12], math.MaxUint32-1)
		return payload
	}())
	moov := extendedAtomBytes("moov", stco)

	patched, err := patchChunkOffsets(append([]byte(nil), moov...), int64(len(moov)))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(patched), len(moov)+4; got != want {
		t.Fatalf("patched size = %d, want %d", got, want)
	}
	if got := binary.BigEndian.Uint32(patched[0:4]); got != 1 {
		t.Fatalf("moov size marker = %d, want extended-size marker", got)
	}
	if got := binary.BigEndian.Uint64(patched[8:16]); got != uint64(len(patched)) {
		t.Fatalf("extended moov size = %d, want %d", got, len(patched))
	}
	converted, err := parseAtomBytes(patched, extendedAtomHeaderSize, len(patched))
	if err != nil {
		t.Fatal(err)
	}
	if converted.typ != atomTypeCO64 || converted.headerSize != extendedAtomHeaderSize {
		t.Fatalf("converted atom = %+v, want extended co64", converted)
	}
	if got := binary.BigEndian.Uint64(patched[extendedAtomHeaderSize+8 : extendedAtomHeaderSize+16]); got != uint64(converted.size) {
		t.Fatalf("extended co64 size = %d, want %d", got, converted.size)
	}
	entryPos := extendedAtomHeaderSize + converted.headerSize + 8
	if got, want := binary.BigEndian.Uint64(patched[entryPos:entryPos+8]), uint64(math.MaxUint32-1)+uint64(len(patched)); got != want {
		t.Fatalf("patched chunk offset = %d, want %d", got, want)
	}
}

func BenchmarkPatchChunkOffsetsInPlace(b *testing.B) {
	original := benchMoov(1<<16, false)
	moov := make([]byte, len(original))
	b.SetBytes(int64(len(original)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		copy(moov, original)
		b.StartTimer()
		if _, err := patchChunkOffsets(moov, int64(len(moov))); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPatchChunkOffsetsConvert(b *testing.B) {
	moov := benchMoov(1<<16, true)
	b.SetBytes(int64(len(moov)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := patchChunkOffsets(moov, int64(len(moov))); err != nil {
			b.Fatal(err)
		}
	}
}

func benchMoov(entries int, overflowing bool) []byte {
	value := uint32(1 << 20)
	if overflowing {
		value = ^uint32(0) - 1024
	}
	table := make([]uint32, entries)
	for i := range table {
		table[i] = value + uint32(i)
	}
	return nestedAtom("moov",
		nestedAtom("trak", nestedAtom("mdia", nestedAtom("minf", nestedAtom("stbl", stcoAtom(table...))))),
	)
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

func extendedAtomBytes(typ string, payload []byte) []byte {
	data := make([]byte, extendedAtomHeaderSize+len(payload))
	binary.BigEndian.PutUint32(data[0:4], 1)
	copy(data[4:8], typ)
	binary.BigEndian.PutUint64(data[8:16], uint64(len(data)))
	copy(data[16:], payload)
	return data
}
