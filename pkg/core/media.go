package core

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/media"
)

// Client-owned value types and client-facing methods, mirroring the pattern
// in client.go: the media implementation lives in pkg/media, and pkg/mobile
// consumes media value types through the core-owned contracts below, never
// through pkg/media. The structs mirror the media definitions field for
// field (same names, types and JSON tags); the client conversion helpers
// below adapt media values into them. JSON round-trip tests in
// media_contract_test.go pin the mobile wire format against the media DTOs.
//
// The legacy Core.ProbeMP4 / Core.OpenVirtualFile methods keep their
// media-typed signatures for source compatibility; the *Client variants
// below are the boundary for client-facing code.
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

// VirtualFileInfo is the core-owned client contract describing a virtual file
// handle.
type VirtualFileInfo struct {
	Mode        string    `json:"mode"`
	Size        int64     `json:"size"`
	Transformed bool      `json:"transformed"`
	MP4         *MP4Probe `json:"mp4,omitempty"`
}

// VirtualReadMapping is the core-owned client contract describing how one span
// of a virtual file maps back onto the source file.
type VirtualReadMapping struct {
	VirtualOffset int64  `json:"virtual_offset"`
	Length        int    `json:"length"`
	Source        string `json:"source"`
	SourceOffset  int64  `json:"source_offset,omitempty"`
}

// VirtualFileHandle is the core-owned client handle over a media virtual
// file. It encapsulates the media implementation value and exposes only the
// operations mobile needs: Info, ReadAtInto, ReadMappings, and Close.
type VirtualFileHandle struct {
	file media.VirtualFile
}

// Info returns the core-owned description of the virtual file layout.
func (h *VirtualFileHandle) Info() VirtualFileInfo {
	return convertVirtualFileInfo(h.file.Info())
}

// ReadAtInto reads up to len(dst) virtual bytes at offset into dst and returns
// the number of bytes written.
func (h *VirtualFileHandle) ReadAtInto(ctx context.Context, offset int64, dst []byte) (int, error) {
	return h.file.ReadAtInto(ctx, offset, dst)
}

// ReadMappings describes how the virtual span [offset, offset+length) maps
// back onto the source file.
func (h *VirtualFileHandle) ReadMappings(offset int64, length int) []VirtualReadMapping {
	return convertReadMappings(h.file.ReadMappings(offset, length))
}

// Close releases the underlying virtual file resources.
func (h *VirtualFileHandle) Close() error {
	return h.file.Close()
}

// ProbeMP4 probes the MP4 layout of path using the media implementation and
// reports it through the legacy media-typed DTO. It is kept source-compatible
// for existing callers; new code should use ProbeMP4Client.
func (c *Core) ProbeMP4(ctx context.Context, path string) (media.MP4Probe, error) {
	item, err := c.Stat(ctx, path)
	if err != nil {
		return media.MP4Probe{}, err
	}
	if item.IsDir {
		return media.MP4Probe{}, fmt.Errorf("core: %s is a directory", path)
	}
	return media.ProbeMP4(ctx, item.Size, func(ctx context.Context, offset int64, length int) ([]byte, error) {
		return c.readAtRange(ctx, path, offset, length)
	})
}

// ProbeMP4Client is the client-facing MP4 probe API. It behaves exactly like
// ProbeMP4 but returns the core-owned MP4Probe contract, so callers (such as
// pkg/mobile) never depend on pkg/media.
func (c *Core) ProbeMP4Client(ctx context.Context, path string) (MP4Probe, error) {
	probe, err := c.ProbeMP4(ctx, path)
	if err != nil {
		return MP4Probe{}, err
	}
	return convertMP4Probe(probe), nil
}

// OpenVirtualFile opens a virtual file over path using the media
// implementation and returns the legacy media-typed VirtualFile. It is kept
// source-compatible for existing callers; new code should use
// OpenVirtualFileClient.
func (c *Core) OpenVirtualFile(ctx context.Context, path, mode string) (media.VirtualFile, error) {
	item, err := c.Stat(ctx, path)
	if err != nil {
		return nil, err
	}
	if item.IsDir {
		return nil, fmt.Errorf("core: %s is a directory", path)
	}
	readAt := func(ctx context.Context, offset int64, length int) ([]byte, error) {
		return c.readAtRange(ctx, path, offset, length)
	}
	readAtInto := func(ctx context.Context, offset int64, dst []byte) (int, error) {
		return c.ReadAtInto(ctx, path, offset, dst, 0)
	}
	return media.NewVirtualFileInto(ctx, mode, item.Size, readAt, readAtInto)
}

// OpenVirtualFileClient is the client-facing virtual-file API. It behaves
// exactly like OpenVirtualFile but wraps the media virtual file in the
// core-owned VirtualFileHandle contract, so callers (such as pkg/mobile)
// never depend on pkg/media.
func (c *Core) OpenVirtualFileClient(ctx context.Context, path, mode string) (*VirtualFileHandle, error) {
	file, err := c.OpenVirtualFile(ctx, path, mode)
	if err != nil {
		return nil, err
	}
	return &VirtualFileHandle{file: file}, nil
}

func convertMP4Probe(p media.MP4Probe) MP4Probe {
	return MP4Probe{
		IsMP4:          p.IsMP4,
		FastStart:      p.FastStart,
		NeedsFastStart: p.NeedsFastStart,
		FtypOffset:     p.FtypOffset,
		FtypSize:       p.FtypSize,
		MoovOffset:     p.MoovOffset,
		MoovSize:       p.MoovSize,
		MdatOffset:     p.MdatOffset,
		MdatSize:       p.MdatSize,
	}
}

func convertVirtualFileInfo(info media.VirtualFileInfo) VirtualFileInfo {
	out := VirtualFileInfo{
		Mode:        info.Mode,
		Size:        info.Size,
		Transformed: info.Transformed,
	}
	if info.MP4 != nil {
		probe := convertMP4Probe(*info.MP4)
		out.MP4 = &probe
	}
	return out
}

func convertReadMappings(mappings []media.VirtualReadMapping) []VirtualReadMapping {
	if mappings == nil {
		return nil
	}
	out := make([]VirtualReadMapping, len(mappings))
	for i, m := range mappings {
		out[i] = VirtualReadMapping{
			VirtualOffset: m.VirtualOffset,
			Length:        m.Length,
			Source:        m.Source,
			SourceOffset:  m.SourceOffset,
		}
	}
	return out
}

func (c *Core) readAtRange(ctx context.Context, path string, offset int64, length int) ([]byte, error) {
	if length <= 0 {
		return []byte{}, nil
	}
	limit := c.readLimit(0)
	data := make([]byte, 0, length)
	for len(data) < length {
		want := min(length-len(data), limit)
		chunk, err := c.ReadAt(ctx, path, offset+int64(len(data)), want, limit)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			break
		}
		data = append(data, chunk...)
		if len(chunk) < want {
			break
		}
	}
	return data, nil
}
