package media

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
)

const (
	atomHeaderSize         = 8
	extendedAtomHeaderSize = 16
)

type ReadAtFunc func(ctx context.Context, offset int64, length int) ([]byte, error)
type ReadAtIntoFunc func(ctx context.Context, offset int64, dst []byte) (int, error)

func readAtIntoFromReadAt(readAt ReadAtFunc) ReadAtIntoFunc {
	return func(ctx context.Context, offset int64, dst []byte) (int, error) {
		if len(dst) == 0 {
			return 0, nil
		}
		data, err := readAt(ctx, offset, len(dst))
		if err != nil {
			return 0, err
		}
		return copy(dst, data), nil
	}
}

type MP4Probe struct {
	IsMP4          bool  `json:"is_mp4"`
	FastStart      bool  `json:"fast_start"`
	NeedsFastStart bool  `json:"needs_fast_start"`
	FtypOffset     int64 `json:"ftyp_offset,omitempty"`
	FtypSize       int64 `json:"ftyp_size,omitempty"`
	MoovOffset     int64 `json:"moov_offset,omitempty"`
	MoovSize       int64 `json:"moov_size,omitempty"`
	MdatOffset     int64 `json:"mdat_offset,omitempty"`
	MdatSize       int64 `json:"mdat_size,omitempty"`
}

type atom struct {
	typ        string
	offset     int64
	size       int64
	headerSize int64
}

func ProbeMP4(ctx context.Context, size int64, readAt ReadAtFunc) (MP4Probe, error) {
	if size < atomHeaderSize {
		return MP4Probe{}, nil
	}
	var out MP4Probe
	for offset := int64(0); offset+atomHeaderSize <= size; {
		a, err := readAtom(ctx, readAt, offset, size)
		if err != nil {
			return out, err
		}
		switch a.typ {
		case "ftyp":
			out.IsMP4 = true
			out.FtypOffset = a.offset
			out.FtypSize = a.size
		case "moov":
			out.MoovOffset = a.offset
			out.MoovSize = a.size
		case "mdat":
			out.MdatOffset = a.offset
			out.MdatSize = a.size
		}
		if out.MoovSize > 0 && out.MdatSize > 0 {
			break
		}
		offset += a.size
	}
	if !out.IsMP4 || out.MoovSize == 0 || out.MdatSize == 0 {
		return out, nil
	}
	out.FastStart = out.MoovOffset < out.MdatOffset
	out.NeedsFastStart = out.MdatOffset < out.MoovOffset
	return out, nil
}

func readAtom(ctx context.Context, readAt ReadAtFunc, offset, fileSize int64) (atom, error) {
	header, err := readAt(ctx, offset, extendedAtomHeaderSize)
	if err != nil {
		return atom{}, err
	}
	if len(header) < atomHeaderSize {
		return atom{}, fmt.Errorf("media: short atom header at %d", offset)
	}
	size32 := binary.BigEndian.Uint32(header[0:4])
	typ := string(header[4:8])
	size := int64(size32)
	headerSize := int64(atomHeaderSize)
	switch size32 {
	case 0:
		size = fileSize - offset
	case 1:
		if len(header) < extendedAtomHeaderSize {
			return atom{}, fmt.Errorf("media: short extended atom header at %d", offset)
		}
		size = int64(binary.BigEndian.Uint64(header[8:16]))
		headerSize = extendedAtomHeaderSize
	}
	if size < headerSize || offset+size > fileSize {
		return atom{}, fmt.Errorf("media: invalid atom %q at %d size %d", typ, offset, size)
	}
	return atom{typ: typ, offset: offset, size: size, headerSize: headerSize}, nil
}

// maxChunkPatchPasses bounds the delta fixed-point iteration. Each pass is a
// cheap metadata rescan (no output is built); the converting-table set can
// only grow, so the sequence stabilizes within a few passes.
const maxChunkPatchPasses = 8

// patchChunkOffsets shifts every chunk offset inside the moov atom by delta
// and returns the patched moov. data must be an exclusively owned buffer: when
// no chunk table needs the stco→co64 conversion the patch happens in place and
// data itself is returned.
//
// Converting stco to co64 grows the moov, which grows the shift itself. That
// fixed point is resolved analytically — each pass recomputes conversion
// decisions and the resulting size from metadata only — so the moov bytes are
// written exactly once at the end.
func patchChunkOffsets(data []byte, delta int64) ([]byte, error) {
	var tables []chunkTable
	if err := scanChunkTables(data, 0, len(data), &tables); err != nil {
		return nil, err
	}

	cur := delta
	converged := false
	for i := 0; i < maxChunkPatchPasses; i++ {
		growth := int64(0)
		for j := range tables {
			t := &tables[j]
			t.converts = !t.isCo64 && cur >= 0 && t.maxEntryValue+uint64(cur) > math.MaxUint32
			if t.converts {
				growth += int64(t.count) * 4
			}
		}
		total := int64(len(data)) + growth
		if total == cur {
			converged = true
			break
		}
		cur = total
	}
	if !converged {
		return nil, fmt.Errorf("media: chunk offset rewrite did not converge")
	}

	converts := false
	for j := range tables {
		if tables[j].converts {
			converts = true
			break
		}
	}
	if !converts {
		return data, patchChunkTablesInPlace(data, tables, cur)
	}

	out := make([]byte, cur)
	cursor := tableCursor{tables: tables}
	written, err := writePatchedAtoms(out, 0, data, 0, len(data), cur, &cursor)
	if err != nil {
		return nil, err
	}
	if written != len(out) {
		return nil, fmt.Errorf("media: chunk offset rewrite produced %d bytes, want %d", written, len(out))
	}
	return out, nil
}

// chunkTable records one stco/co64 atom found during the scan. offset is the
// atom's absolute position within the source buffer.
type chunkTable struct {
	offset        int
	entriesOffset int
	isCo64        bool
	count         int
	maxEntryValue uint64
	converts      bool
}

func scanChunkTables(data []byte, start, end int, tables *[]chunkTable) error {
	parsed := false
	for offset := start; offset+atomHeaderSize <= end; {
		a, err := parseAtomBytes(data, offset, end)
		if err != nil {
			return err
		}
		parsed = true
		switch a.typ {
		case atomTypeSTCO, atomTypeCO64:
			t, err := parseChunkTable(data[offset:offset+a.size], a)
			if err != nil {
				return err
			}
			t.offset = offset
			*tables = append(*tables, t)
		default:
			childStart := a.headerSize
			if a.typ == atomTypeMeta {
				childStart += 4
			}
			if isContainerAtom(a.typ) && childStart < a.size {
				if err := scanChunkTables(data, offset+childStart, offset+a.size, tables); err != nil {
					return err
				}
			}
		}
		offset += a.size
	}
	if !parsed && start < end {
		return fmt.Errorf("media: invalid trailing atom data")
	}
	return nil
}

func parseChunkTable(atom []byte, a byteAtom) (chunkTable, error) {
	co64 := a.typ == atomTypeCO64
	name, entrySize := "stco", 4
	if co64 {
		name, entrySize = "co64", 8
	}
	entriesOffset := a.headerSize + 8
	if len(atom) < entriesOffset {
		return chunkTable{}, fmt.Errorf("media: short %s atom", name)
	}
	count := int(binary.BigEndian.Uint32(atom[a.headerSize+4 : entriesOffset]))
	if int64(len(atom)) < int64(entriesOffset)+int64(count)*int64(entrySize) {
		return chunkTable{}, fmt.Errorf("media: truncated %s entries", name)
	}
	t := chunkTable{entriesOffset: entriesOffset, isCo64: co64, count: count}
	for i, pos := 0, entriesOffset; i < count; i, pos = i+1, pos+entrySize {
		v := uint64(binary.BigEndian.Uint32(atom[pos : pos+4]))
		if co64 {
			v = binary.BigEndian.Uint64(atom[pos : pos+8])
		}
		if v > t.maxEntryValue {
			t.maxEntryValue = v
		}
	}
	return t, nil
}

// patchChunkTablesInPlace applies delta to every recorded table directly in
// data. All values are validated before any byte is written.
func patchChunkTablesInPlace(data []byte, tables []chunkTable, delta int64) error {
	for i := range tables {
		t := &tables[i]
		pos := t.offset + t.entriesOffset
		if t.isCo64 {
			if delta < 0 {
				return fmt.Errorf("media: negative co64 delta %d", delta)
			}
			add := uint64(delta)
			for j := 0; j < t.count; j, pos = j+1, pos+8 {
				v := binary.BigEndian.Uint64(data[pos : pos+8])
				if v > math.MaxUint64-add {
					return fmt.Errorf("media: co64 offset overflow: value=%d delta=%d", v, delta)
				}
			}
			continue
		}
		for j := 0; j < t.count; j, pos = j+1, pos+4 {
			if int64(binary.BigEndian.Uint32(data[pos:pos+4]))+delta < 0 {
				return fmt.Errorf("media: stco offset underflow: value=%d delta=%d",
					binary.BigEndian.Uint32(data[pos:pos+4]), delta)
			}
		}
	}
	add := uint64(delta)
	for i := range tables {
		t := &tables[i]
		pos := t.offset + t.entriesOffset
		if t.isCo64 {
			for j := 0; j < t.count; j, pos = j+1, pos+8 {
				v := binary.BigEndian.Uint64(data[pos : pos+8])
				binary.BigEndian.PutUint64(data[pos:pos+8], v+add)
			}
			continue
		}
		for j := 0; j < t.count; j, pos = j+1, pos+4 {
			v := binary.BigEndian.Uint32(data[pos : pos+4])
			binary.BigEndian.PutUint32(data[pos:pos+4], v+uint32(delta))
		}
	}
	return nil
}

// tableCursor hands the precomputed conversion decisions to the writer in the
// same order the scan discovered them.
type tableCursor struct {
	tables []chunkTable
	next   int
}

// writePatchedAtoms writes the [start,end) region of data into out beginning
// at pos, shifting chunk offsets by delta and emitting converted tables as
// co64. Every source byte is copied exactly once; container sizes are fixed
// up after their children are written. It returns the new write position.
func writePatchedAtoms(out []byte, pos int, data []byte, start, end int, delta int64, cursor *tableCursor) (int, error) {
	pos0 := pos
	for offset := start; offset+atomHeaderSize <= end; {
		a, err := parseAtomBytes(data, offset, end)
		if err != nil {
			return pos, err
		}
		switch a.typ {
		case atomTypeSTCO, atomTypeCO64:
			t := cursor.tables[cursor.next]
			cursor.next++
			w, err := writeChunkTable(out[pos:], data[offset:offset+a.size], a, t, delta)
			if err != nil {
				return pos, err
			}
			pos += w
		default:
			childStart := a.headerSize
			isMeta := a.typ == atomTypeMeta
			if isMeta {
				childStart += 4
			}
			if !isContainerAtom(a.typ) || childStart >= a.size {
				pos += copy(out[pos:], data[offset:offset+a.size])
				offset += a.size
				continue
			}
			headerPos := pos
			pos += a.headerSize
			copy(out[headerPos:pos], data[offset:offset+a.headerSize])
			if isMeta {
				pos += copy(out[pos:], data[offset+a.headerSize:offset+childStart])
			}
			pos, err = writePatchedAtoms(out, pos, data, offset+childStart, offset+a.size, delta, cursor)
			if err != nil {
				return headerPos, err
			}
			size := pos - headerPos
			if a.headerSize == extendedAtomHeaderSize {
				binary.BigEndian.PutUint32(out[headerPos:headerPos+4], 1)
				binary.BigEndian.PutUint64(out[headerPos+8:headerPos+16], uint64(size))
			} else {
				if int64(size) > math.MaxUint32 {
					return headerPos, fmt.Errorf("media: rewritten atom %q too large", a.typ)
				}
				binary.BigEndian.PutUint32(out[headerPos:headerPos+4], uint32(size))
			}
		}
		offset += a.size
	}
	if pos == pos0 && start < end {
		return pos, fmt.Errorf("media: invalid trailing atom data")
	}
	return pos, nil
}

// writeChunkTable patches one chunk table into dst, converting it from stco
// to co64 when the scan decided it no longer fits uint32. It returns the
// number of bytes written.
func writeChunkTable(dst []byte, src []byte, a byteAtom, t chunkTable, delta int64) (int, error) {
	if !t.converts {
		n := copy(dst, src)
		if a.typ == atomTypeCO64 {
			if delta < 0 {
				return 0, fmt.Errorf("media: negative co64 delta %d", delta)
			}
			add := uint64(delta)
			for j, pos := 0, t.entriesOffset; j < t.count; j, pos = j+1, pos+8 {
				v := binary.BigEndian.Uint64(src[pos : pos+8])
				if v > math.MaxUint64-add {
					return 0, fmt.Errorf("media: co64 offset overflow: value=%d delta=%d", v, delta)
				}
				binary.BigEndian.PutUint64(dst[pos:pos+8], v+add)
			}
			return n, nil
		}
		for j, pos := 0, t.entriesOffset; j < t.count; j, pos = j+1, pos+4 {
			v := int64(binary.BigEndian.Uint32(src[pos:pos+4])) + delta
			if v < 0 {
				return 0, fmt.Errorf("media: stco offset underflow: value=%d delta=%d", v-delta, delta)
			}
			binary.BigEndian.PutUint32(dst[pos:pos+4], uint32(v))
		}
		return n, nil
	}

	size := a.size + 4*t.count
	if a.headerSize == atomHeaderSize && int64(size) > math.MaxUint32 {
		return 0, fmt.Errorf("media: rewritten atom %q too large", atomTypeCO64)
	}
	copy(dst[:a.headerSize], src[:a.headerSize])
	if a.headerSize == extendedAtomHeaderSize {
		binary.BigEndian.PutUint32(dst[0:4], 1)
		binary.BigEndian.PutUint64(dst[8:16], uint64(size))
	} else {
		binary.BigEndian.PutUint32(dst[0:4], uint32(size))
	}
	copy(dst[4:8], "co64")
	copy(dst[a.headerSize:a.headerSize+4], src[a.headerSize:a.headerSize+4])
	binary.BigEndian.PutUint32(dst[a.headerSize+4:a.headerSize+8], uint32(t.count))
	for j, sp, dp := 0, t.entriesOffset, t.entriesOffset; j < t.count; j, sp, dp = j+1, sp+4, dp+8 {
		v := int64(binary.BigEndian.Uint32(src[sp:sp+4])) + delta
		if v < 0 {
			return 0, fmt.Errorf("media: stco offset underflow: value=%d delta=%d", v-delta, delta)
		}
		binary.BigEndian.PutUint64(dst[dp:dp+8], uint64(v))
	}
	return size, nil
}

// atomType compares box type names without the allocation of string([]byte).
type atomType [4]byte

var (
	atomTypeSTCO = atomType{'s', 't', 'c', 'o'}
	atomTypeCO64 = atomType{'c', 'o', '6', '4'}
	atomTypeMeta = atomType{'m', 'e', 't', 'a'}
	atomTypeMoov = atomType{'m', 'o', 'o', 'v'}
	atomTypeTrak = atomType{'t', 'r', 'a', 'k'}
	atomTypeMdia = atomType{'m', 'd', 'i', 'a'}
	atomTypeMinf = atomType{'m', 'i', 'n', 'f'}
	atomTypeStbl = atomType{'s', 't', 'b', 'l'}
	atomTypeEdts = atomType{'e', 'd', 't', 's'}
	atomTypeUdta = atomType{'u', 'd', 't', 'a'}
)

func (t atomType) String() string { return string(t[:]) }

// byteAtom is a parsed atom header. size counts the whole atom, headerSize is
// 8, or 16 for extended-size atoms.
type byteAtom struct {
	typ        atomType
	size       int
	headerSize int
}

func parseAtomBytes(data []byte, offset, end int) (byteAtom, error) {
	size32 := binary.BigEndian.Uint32(data[offset : offset+4])
	var typ atomType
	copy(typ[:], data[offset+4:offset+8])
	size := int(size32)
	headerSize := atomHeaderSize
	switch size32 {
	case 1:
		if offset+extendedAtomHeaderSize > end {
			return byteAtom{}, fmt.Errorf("media: short extended atom header in %q", typ)
		}
		size64 := binary.BigEndian.Uint64(data[offset+8 : offset+16])
		if size64 > uint64(^uint(0)>>1) {
			return byteAtom{}, fmt.Errorf("media: atom %q too large", typ)
		}
		size = int(size64)
		headerSize = extendedAtomHeaderSize
	case 0:
		size = end - offset
	}
	if size < headerSize || offset+size > end {
		return byteAtom{}, fmt.Errorf("media: invalid atom %q size %d", typ, size)
	}
	return byteAtom{typ: typ, size: size, headerSize: headerSize}, nil
}

func isContainerAtom(typ atomType) bool {
	switch typ {
	case atomTypeMoov, atomTypeTrak, atomTypeMdia, atomTypeMinf,
		atomTypeStbl, atomTypeEdts, atomTypeUdta, atomTypeMeta:
		return true
	default:
		return false
	}
}
