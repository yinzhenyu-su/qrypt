package media

import (
	"context"
	"fmt"
)

type MP4FastStartNotNeededError struct {
	Probe MP4Probe
}

func (e MP4FastStartNotNeededError) Error() string {
	return "media: mp4 faststart virtual file not needed"
}

type MP4FastStartVirtualFile struct {
	size             int64
	probe            MP4Probe
	ftyp             []byte
	moov             []byte
	mdatVirtualStart int64
	readAt           ReadAtFunc
	readAtInto       ReadAtIntoFunc
}

func NewMP4FastStartVirtualFile(ctx context.Context, size int64, readAt ReadAtFunc) (*MP4FastStartVirtualFile, error) {
	return NewMP4FastStartVirtualFileInto(ctx, size, readAt, readAtIntoFromReadAt(readAt))
}

func NewMP4FastStartVirtualFileInto(ctx context.Context, size int64, readAt ReadAtFunc, readAtInto ReadAtIntoFunc) (*MP4FastStartVirtualFile, error) {
	probe, err := ProbeMP4(ctx, size, readAt)
	if err != nil {
		return nil, err
	}
	if !probe.IsMP4 || !probe.NeedsFastStart {
		return nil, MP4FastStartNotNeededError{Probe: probe}
	}
	if probe.FtypOffset != 0 || probe.FtypSize == 0 || probe.MdatSize == 0 || probe.MoovSize == 0 {
		return nil, fmt.Errorf("media: unsupported mp4 atom layout")
	}
	if probe.MdatOffset != probe.FtypSize {
		return nil, fmt.Errorf("media: unsupported mp4 layout with atoms before mdat")
	}
	if probe.MoovSize > int64(^uint(0)>>1) || probe.FtypSize > int64(^uint(0)>>1) {
		return nil, fmt.Errorf("media: mp4 atom too large")
	}

	ftyp, err := readAt(ctx, probe.FtypOffset, int(probe.FtypSize))
	if err != nil {
		return nil, fmt.Errorf("media: read ftyp: %w", err)
	}
	if int64(len(ftyp)) != probe.FtypSize {
		return nil, fmt.Errorf("media: short ftyp read")
	}
	moov, err := readAt(ctx, probe.MoovOffset, int(probe.MoovSize))
	if err != nil {
		return nil, fmt.Errorf("media: read moov: %w", err)
	}
	if int64(len(moov)) != probe.MoovSize {
		return nil, fmt.Errorf("media: short moov read")
	}
	moov, err = patchChunkOffsets(moov, probe.MoovSize)
	if err != nil {
		return nil, err
	}
	return &MP4FastStartVirtualFile{
		size:             probe.FtypSize + int64(len(moov)) + probe.MdatSize,
		probe:            probe,
		ftyp:             ftyp,
		moov:             moov,
		mdatVirtualStart: probe.FtypSize + int64(len(moov)),
		readAt:           readAt,
		readAtInto:       readAtInto,
	}, nil
}

func (v *MP4FastStartVirtualFile) Info() VirtualFileInfo {
	return VirtualFileInfo{
		Mode:        VirtualModeMP4FastStart,
		Size:        v.size,
		Transformed: true,
		MP4:         &v.probe,
	}
}

func (v *MP4FastStartVirtualFile) ReadAt(ctx context.Context, offset int64, length int) ([]byte, error) {
	if offset < 0 {
		return nil, fmt.Errorf("media: offset must be non-negative")
	}
	if length < 0 {
		return nil, fmt.Errorf("media: length must be non-negative")
	}
	if length == 0 || offset >= v.size {
		return []byte{}, nil
	}
	if maxLen := v.size - offset; int64(length) > maxLen {
		length = int(maxLen)
	}

	out := make([]byte, length)
	n, err := v.ReadAtInto(ctx, offset, out)
	if err != nil {
		return nil, err
	}
	return out[:n], nil
}

func (v *MP4FastStartVirtualFile) ReadAtInto(ctx context.Context, offset int64, dst []byte) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("media: offset must be non-negative")
	}
	if len(dst) == 0 || offset >= v.size {
		return 0, nil
	}
	if maxLen := v.size - offset; int64(len(dst)) > maxLen {
		dst = dst[:maxLen]
	}

	cursor := offset
	written := 0
	remaining := len(dst)
	for remaining > 0 {
		if cursor < int64(len(v.ftyp)) {
			n := copyFromBytes(dst[written:], v.ftyp, cursor, remaining)
			cursor += int64(n)
			remaining -= n
			written += n
			continue
		}
		moovStart := int64(len(v.ftyp))
		moovEnd := moovStart + int64(len(v.moov))
		if cursor < moovEnd {
			n := copyFromBytes(dst[written:], v.moov, cursor-moovStart, remaining)
			cursor += int64(n)
			remaining -= n
			written += n
			continue
		}
		rawOffset := v.probe.MdatOffset + (cursor - v.mdatVirtualStart)
		n, err := v.readAtInto(ctx, rawOffset, dst[written:written+remaining])
		if err != nil {
			return written + n, err
		}
		if n == 0 {
			break
		}
		cursor += int64(n)
		remaining -= n
		written += n
	}
	return written, nil
}

func (v *MP4FastStartVirtualFile) ReadMappings(offset int64, length int) []VirtualReadMapping {
	if offset < 0 || length <= 0 || offset >= v.size {
		return nil
	}
	if maxLen := v.size - offset; int64(length) > maxLen {
		length = int(maxLen)
	}

	var mappings []VirtualReadMapping
	cursor := offset
	remaining := length
	for remaining > 0 {
		if cursor < int64(len(v.ftyp)) {
			n := segmentLength(cursor, remaining, int64(len(v.ftyp)))
			mappings = append(mappings, VirtualReadMapping{
				VirtualOffset: cursor,
				Length:        n,
				Source:        "ftyp",
				SourceOffset:  v.probe.FtypOffset + cursor,
			})
			cursor += int64(n)
			remaining -= n
			continue
		}
		moovStart := int64(len(v.ftyp))
		moovEnd := moovStart + int64(len(v.moov))
		if cursor < moovEnd {
			n := segmentLength(cursor, remaining, moovEnd)
			mappings = append(mappings, VirtualReadMapping{
				VirtualOffset: cursor,
				Length:        n,
				Source:        "moov",
				SourceOffset:  v.probe.MoovOffset + (cursor - moovStart),
			})
			cursor += int64(n)
			remaining -= n
			continue
		}
		n := remaining
		mappings = append(mappings, VirtualReadMapping{
			VirtualOffset: cursor,
			Length:        n,
			Source:        "mdat",
			SourceOffset:  v.probe.MdatOffset + (cursor - v.mdatVirtualStart),
		})
		break
	}
	return mappings
}

func (v *MP4FastStartVirtualFile) Close() error {
	v.ftyp = nil
	v.moov = nil
	v.readAt = nil
	v.readAtInto = nil
	return nil
}

func copyFromBytes(dst []byte, source []byte, offset int64, length int) int {
	if offset >= int64(len(source)) || length <= 0 {
		return 0
	}
	end := offset + int64(length)
	if end > int64(len(source)) {
		end = int64(len(source))
	}
	return copy(dst, source[offset:end])
}

func segmentLength(cursor int64, remaining int, segmentEnd int64) int {
	n := segmentEnd - cursor
	if n > int64(remaining) {
		n = int64(remaining)
	}
	return int(n)
}
