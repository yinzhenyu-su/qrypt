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

type VirtualFile interface {
	Info() VirtualFileInfo
	ReadAt(ctx context.Context, offset int64, length int) ([]byte, error)
	Close() error
}

type passthroughVirtualFile struct {
	size   int64
	readAt ReadAtFunc
}

func NewPassthroughVirtualFile(size int64, readAt ReadAtFunc) VirtualFile {
	return &passthroughVirtualFile{size: size, readAt: readAt}
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
	if max := v.size - offset; int64(length) > max {
		length = int(max)
	}
	return v.readAt(ctx, offset, length)
}

func (v *passthroughVirtualFile) Close() error {
	return nil
}

func NewVirtualFile(ctx context.Context, mode string, size int64, readAt ReadAtFunc) (VirtualFile, error) {
	switch mode {
	case "", VirtualModeAutoMedia:
		mp4, err := NewMP4FastStartVirtualFile(ctx, size, readAt)
		if err == nil {
			return mp4, nil
		}
		if _, ok := err.(MP4FastStartNotNeededError); ok {
			return NewPassthroughVirtualFile(size, readAt), nil
		}
		return NewPassthroughVirtualFile(size, readAt), nil
	case VirtualModePassthrough:
		return NewPassthroughVirtualFile(size, readAt), nil
	case VirtualModeMP4FastStart:
		return NewMP4FastStartVirtualFile(ctx, size, readAt)
	default:
		return nil, fmt.Errorf("media: unknown virtual file mode %q", mode)
	}
}
