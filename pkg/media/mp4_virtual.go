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
}

func NewMP4FastStartVirtualFile(ctx context.Context, size int64, readAt ReadAtFunc) (*MP4FastStartVirtualFile, error) {
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
	patchChunkOffsets(moov, probe.MoovSize)
	return &MP4FastStartVirtualFile{
		size:             probe.FtypSize + probe.MoovSize + probe.MdatSize,
		probe:            probe,
		ftyp:             ftyp,
		moov:             moov,
		mdatVirtualStart: probe.FtypSize + probe.MoovSize,
		readAt:           readAt,
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
	if max := v.size - offset; int64(length) > max {
		length = int(max)
	}

	out := make([]byte, 0, length)
	cursor := offset
	remaining := length
	for remaining > 0 {
		if cursor < int64(len(v.ftyp)) {
			n := copyFromBytes(&out, v.ftyp, cursor, remaining)
			cursor += int64(n)
			remaining -= n
			continue
		}
		moovStart := int64(len(v.ftyp))
		moovEnd := moovStart + int64(len(v.moov))
		if cursor < moovEnd {
			n := copyFromBytes(&out, v.moov, cursor-moovStart, remaining)
			cursor += int64(n)
			remaining -= n
			continue
		}
		rawOffset := v.probe.MdatOffset + (cursor - v.mdatVirtualStart)
		data, err := v.readAt(ctx, rawOffset, remaining)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			break
		}
		out = append(out, data...)
		cursor += int64(len(data))
		remaining -= len(data)
	}
	return out, nil
}

func (v *MP4FastStartVirtualFile) Close() error {
	v.ftyp = nil
	v.moov = nil
	v.readAt = nil
	return nil
}

func copyFromBytes(out *[]byte, source []byte, offset int64, length int) int {
	if offset >= int64(len(source)) || length <= 0 {
		return 0
	}
	end := offset + int64(length)
	if end > int64(len(source)) {
		end = int64(len(source))
	}
	*out = append(*out, source[offset:end]...)
	return int(end - offset)
}
