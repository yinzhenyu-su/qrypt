package media

import (
	"context"
	"fmt"
)

const (
	VirtualModeAutoMedia    = "auto_media"
	VirtualModePassthrough  = "passthrough"
	VirtualModeMP4FastStart = "mp4_faststart"
)

type VirtualFileInfo struct {
	Mode        string    `json:"mode"`
	Size        int64     `json:"size"`
	Transformed bool      `json:"transformed"`
	MP4         *MP4Probe `json:"mp4,omitempty"`
}

type VirtualReadMapping struct {
	VirtualOffset int64  `json:"virtual_offset"`
	Length        int    `json:"length"`
	Source        string `json:"source"`
	SourceOffset  int64  `json:"source_offset,omitempty"`
}

type VirtualFile interface {
	Info() VirtualFileInfo
	ReadAt(ctx context.Context, offset int64, length int) ([]byte, error)
	ReadAtInto(ctx context.Context, offset int64, dst []byte) (int, error)
	ReadMappings(offset int64, length int) []VirtualReadMapping
	Close() error
}

type passthroughVirtualFile struct {
	size       int64
	readAt     ReadAtFunc
	readAtInto ReadAtIntoFunc
}

func NewPassthroughVirtualFile(size int64, readAt ReadAtFunc) VirtualFile {
	return NewPassthroughVirtualFileInto(size, readAt, readAtIntoFromReadAt(readAt))
}

func NewPassthroughVirtualFileInto(size int64, readAt ReadAtFunc, readAtInto ReadAtIntoFunc) VirtualFile {
	return &passthroughVirtualFile{size: size, readAt: readAt, readAtInto: readAtInto}
}

func (v *passthroughVirtualFile) Info() VirtualFileInfo {
	return VirtualFileInfo{Mode: VirtualModePassthrough, Size: v.size}
}

func (v *passthroughVirtualFile) ReadAt(ctx context.Context, offset int64, length int) ([]byte, error) {
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
	return v.readAt(ctx, offset, length)
}

func (v *passthroughVirtualFile) ReadAtInto(ctx context.Context, offset int64, dst []byte) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("media: offset must be non-negative")
	}
	if len(dst) == 0 || offset >= v.size {
		return 0, nil
	}
	if maxLen := v.size - offset; int64(len(dst)) > maxLen {
		dst = dst[:maxLen]
	}
	return v.readAtInto(ctx, offset, dst)
}

func (v *passthroughVirtualFile) ReadMappings(offset int64, length int) []VirtualReadMapping {
	if offset < 0 || length <= 0 || offset >= v.size {
		return nil
	}
	if maxLen := v.size - offset; int64(length) > maxLen {
		length = int(maxLen)
	}
	return []VirtualReadMapping{{
		VirtualOffset: offset,
		Length:        length,
		Source:        "passthrough",
		SourceOffset:  offset,
	}}
}

func (v *passthroughVirtualFile) Close() error {
	return nil
}

func NewVirtualFile(ctx context.Context, mode string, size int64, readAt ReadAtFunc) (VirtualFile, error) {
	return NewVirtualFileInto(ctx, mode, size, readAt, readAtIntoFromReadAt(readAt))
}

func NewVirtualFileInto(ctx context.Context, mode string, size int64, readAt ReadAtFunc, readAtInto ReadAtIntoFunc) (VirtualFile, error) {
	switch mode {
	case "", VirtualModeAutoMedia:
		mp4, err := NewMP4FastStartVirtualFileInto(ctx, size, readAt, readAtInto)
		if err == nil {
			return mp4, nil
		}
		if _, ok := err.(MP4FastStartNotNeededError); ok {
			return NewPassthroughVirtualFileInto(size, readAt, readAtInto), nil
		}
		return NewPassthroughVirtualFileInto(size, readAt, readAtInto), nil
	case VirtualModePassthrough:
		return NewPassthroughVirtualFileInto(size, readAt, readAtInto), nil
	case VirtualModeMP4FastStart:
		return NewMP4FastStartVirtualFileInto(ctx, size, readAt, readAtInto)
	default:
		return nil, fmt.Errorf("media: unknown virtual file mode %q", mode)
	}
}
