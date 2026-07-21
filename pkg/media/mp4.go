package media

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	atomHeaderSize         = 8
	extendedAtomHeaderSize = 16
	copyChunkSize          = 1 << 20
)

type ReadAtFunc func(ctx context.Context, offset int64, length int) ([]byte, error)

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

func WriteFastStartMP4(ctx context.Context, size int64, readAt ReadAtFunc, out io.Writer) (MP4Probe, error) {
	probe, err := ProbeMP4(ctx, size, readAt)
	if err != nil {
		return probe, err
	}
	if !probe.IsMP4 {
		return probe, fmt.Errorf("media: not an mp4 file")
	}
	if probe.MoovSize == 0 || probe.MdatSize == 0 || probe.FtypSize == 0 {
		return probe, fmt.Errorf("media: unsupported mp4 atom layout")
	}
	if !probe.NeedsFastStart {
		return probe, fmt.Errorf("media: mp4 already faststart")
	}
	if probe.MoovSize > int64(^uint(0)>>1) {
		return probe, fmt.Errorf("media: moov atom too large")
	}

	moov, err := readAt(ctx, probe.MoovOffset, int(probe.MoovSize))
	if err != nil {
		return probe, fmt.Errorf("media: read moov: %w", err)
	}
	if int64(len(moov)) != probe.MoovSize {
		return probe, fmt.Errorf("media: short moov read")
	}
	patchChunkOffsets(moov, probe.MoovSize)

	if err := copyRange(ctx, readAt, out, probe.FtypOffset, probe.FtypSize); err != nil {
		return probe, err
	}
	if _, err := out.Write(moov); err != nil {
		return probe, err
	}
	if err := copyRange(ctx, readAt, out, probe.MdatOffset, probe.MdatSize); err != nil {
		return probe, err
	}
	return probe, nil
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

func patchChunkOffsets(data []byte, delta int64) {
	walkAtoms(data, 0, len(data), func(typ string, start, size int) {
		switch typ {
		case "stco":
			patchSTCO(data[start:start+size], delta)
		case "co64":
			patchCO64(data[start:start+size], delta)
		}
	})
}

func walkAtoms(data []byte, start, end int, visit func(typ string, start, size int)) {
	for offset := start; offset+atomHeaderSize <= end; {
		size32 := binary.BigEndian.Uint32(data[offset : offset+4])
		typ := string(data[offset+4 : offset+8])
		size := int(size32)
		headerSize := atomHeaderSize
		if size32 == 1 {
			if offset+extendedAtomHeaderSize > end {
				return
			}
			size64 := binary.BigEndian.Uint64(data[offset+8 : offset+16])
			if size64 > uint64(^uint(0)>>1) {
				return
			}
			size = int(size64)
			headerSize = extendedAtomHeaderSize
		} else if size32 == 0 {
			size = end - offset
		}
		if size < headerSize || offset+size > end {
			return
		}
		visit(typ, offset, size)
		childStart := offset + headerSize
		if typ == "meta" {
			childStart += 4
		}
		if isContainerAtom(typ) && childStart < offset+size {
			walkAtoms(data, childStart, offset+size, visit)
		}
		offset += size
	}
}

func isContainerAtom(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "udta", "meta":
		return true
	default:
		return false
	}
}

func patchSTCO(atom []byte, delta int64) {
	if len(atom) < 16 {
		return
	}
	count := int(binary.BigEndian.Uint32(atom[12:16]))
	pos := 16
	for i := 0; i < count && pos+4 <= len(atom); i++ {
		value := int64(binary.BigEndian.Uint32(atom[pos : pos+4]))
		patched := value + delta
		if patched >= 0 && patched <= int64(^uint32(0)) {
			binary.BigEndian.PutUint32(atom[pos:pos+4], uint32(patched))
		}
		pos += 4
	}
}

func patchCO64(atom []byte, delta int64) {
	if len(atom) < 16 {
		return
	}
	count := int(binary.BigEndian.Uint32(atom[12:16]))
	pos := 16
	for i := 0; i < count && pos+8 <= len(atom); i++ {
		value := int64(binary.BigEndian.Uint64(atom[pos : pos+8]))
		patched := value + delta
		if patched >= 0 {
			binary.BigEndian.PutUint64(atom[pos:pos+8], uint64(patched))
		}
		pos += 8
	}
}

func copyRange(ctx context.Context, readAt ReadAtFunc, out io.Writer, offset, size int64) error {
	remaining := size
	cursor := offset
	for remaining > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		want := copyChunkSize
		if remaining < int64(want) {
			want = int(remaining)
		}
		data, err := readAt(ctx, cursor, want)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return io.ErrUnexpectedEOF
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
		cursor += int64(len(data))
		remaining -= int64(len(data))
	}
	return nil
}
