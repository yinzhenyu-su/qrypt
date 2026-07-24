package media

import (
	"bytes"
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

func patchChunkOffsets(data []byte, delta int64) ([]byte, error) {
	currentDelta := delta
	for i := 0; i < 8; i++ {
		patched, err := rewriteChunkOffsets(data, 0, len(data), currentDelta)
		if err != nil {
			return nil, err
		}
		if int64(len(patched)) == currentDelta {
			return patched, nil
		}
		currentDelta = int64(len(patched))
	}
	return nil, fmt.Errorf("media: chunk offset rewrite did not converge")
}

func rewriteChunkOffsets(data []byte, start, end int, delta int64) ([]byte, error) {
	var out bytes.Buffer
	for offset := start; offset+atomHeaderSize <= end; {
		a, err := parseAtomBytes(data, offset, end)
		if err != nil {
			return nil, err
		}
		rewritten, err := rewriteChunkOffsetAtom(data[offset:offset+a.size], a, delta)
		if err != nil {
			return nil, err
		}
		out.Write(rewritten)
		offset += a.size
	}
	if out.Len() == 0 && start < end {
		return nil, fmt.Errorf("media: invalid trailing atom data")
	}
	return out.Bytes(), nil
}

type byteAtom struct {
	typ        string
	size       int
	headerSize int
}

func parseAtomBytes(data []byte, offset, end int) (byteAtom, error) {
	size32 := binary.BigEndian.Uint32(data[offset : offset+4])
	typ := string(data[offset+4 : offset+8])
	size := int(size32)
	headerSize := atomHeaderSize
	if size32 == 1 {
		if offset+extendedAtomHeaderSize > end {
			return byteAtom{}, fmt.Errorf("media: short extended atom header in %q", typ)
		}
		size64 := binary.BigEndian.Uint64(data[offset+8 : offset+16])
		if size64 > uint64(^uint(0)>>1) {
			return byteAtom{}, fmt.Errorf("media: atom %q too large", typ)
		}
		size = int(size64)
		headerSize = extendedAtomHeaderSize
	} else if size32 == 0 {
		size = end - offset
	}
	if size < headerSize || offset+size > end {
		return byteAtom{}, fmt.Errorf("media: invalid atom %q size %d", typ, size)
	}
	return byteAtom{typ: typ, size: size, headerSize: headerSize}, nil
}

func rewriteChunkOffsetAtom(atom []byte, a byteAtom, delta int64) ([]byte, error) {
	switch a.typ {
	case "stco":
		return rewriteSTCO(atom, delta)
	case "co64":
		return rewriteCO64(atom, delta)
	}
	childStart := a.headerSize
	if a.typ == "meta" {
		childStart += 4
	}
	if !isContainerAtom(a.typ) || childStart >= len(atom) {
		return append([]byte(nil), atom...), nil
	}
	children, err := rewriteChunkOffsets(atom, childStart, len(atom), delta)
	if err != nil {
		return nil, err
	}
	payload := append([]byte(nil), atom[a.headerSize:childStart]...)
	payload = append(payload, children...)
	return makeAtom(a.typ, payload)
}

func isContainerAtom(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "udta", "meta":
		return true
	default:
		return false
	}
}

func rewriteSTCO(atom []byte, delta int64) ([]byte, error) {
	if len(atom) < 16 {
		return nil, fmt.Errorf("media: short stco atom")
	}
	count := int(binary.BigEndian.Uint32(atom[12:16]))
	if len(atom) < 16+count*4 {
		return nil, fmt.Errorf("media: truncated stco entries")
	}
	values := make([]uint64, count)
	convert := false
	for i, pos := 0, 16; i < count; i, pos = i+1, pos+4 {
		value := int64(binary.BigEndian.Uint32(atom[pos : pos+4]))
		patched := value + delta
		if patched < 0 {
			return nil, fmt.Errorf("media: stco offset underflow: value=%d delta=%d", value, delta)
		}
		if patched > int64(math.MaxUint32) {
			convert = true
		}
		values[i] = uint64(patched)
	}
	if convert {
		payload := make([]byte, 8+count*8)
		copy(payload[0:4], atom[8:12])
		binary.BigEndian.PutUint32(payload[4:8], uint32(count))
		for i, pos := 0, 8; i < count; i, pos = i+1, pos+8 {
			binary.BigEndian.PutUint64(payload[pos:pos+8], values[i])
		}
		return makeAtom("co64", payload)
	}
	out := append([]byte(nil), atom...)
	for i, pos := 0, 16; i < count; i, pos = i+1, pos+4 {
		binary.BigEndian.PutUint32(out[pos:pos+4], uint32(values[i]))
	}
	return out, nil
}

func rewriteCO64(atom []byte, delta int64) ([]byte, error) {
	if len(atom) < 16 {
		return nil, fmt.Errorf("media: short co64 atom")
	}
	count := int(binary.BigEndian.Uint32(atom[12:16]))
	if len(atom) < 16+count*8 {
		return nil, fmt.Errorf("media: truncated co64 entries")
	}
	if delta < 0 {
		return nil, fmt.Errorf("media: negative co64 delta %d", delta)
	}
	out := append([]byte(nil), atom...)
	add := uint64(delta)
	for i, pos := 0, 16; i < count; i, pos = i+1, pos+8 {
		value := binary.BigEndian.Uint64(out[pos : pos+8])
		if value > math.MaxUint64-add {
			return nil, fmt.Errorf("media: co64 offset overflow: value=%d delta=%d", value, delta)
		}
		binary.BigEndian.PutUint64(out[pos:pos+8], value+add)
	}
	return out, nil
}

func makeAtom(typ string, payload []byte) ([]byte, error) {
	size := atomHeaderSize + len(payload)
	if uint64(size) > math.MaxUint32 {
		return nil, fmt.Errorf("media: rewritten atom %q too large", typ)
	}
	out := make([]byte, size)
	binary.BigEndian.PutUint32(out[0:4], uint32(size))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out, nil
}
