package vfs

import (
	"context"
	"slices"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type CapabilityInfo struct {
	Mount        string             `json:"mount,omitempty"`
	Path         string             `json:"path"`
	Root         bool               `json:"root"`
	MountRoot    bool               `json:"mount_root"`
	Capabilities []drive.Capability `json:"capabilities,omitempty"`
	CanRead      bool               `json:"can_read"`
	CanList      bool               `json:"can_list"`
	CanUpload    bool               `json:"can_upload"`
	CanMkdir     bool               `json:"can_mkdir"`
	CanRename    bool               `json:"can_rename"`
	CanMove      bool               `json:"can_move"`
	CanRemove    bool               `json:"can_remove"`
	CanSpace     bool               `json:"can_space"`
}

type CapabilityReporter interface {
	CapabilitiesForPath(ctx context.Context, path string) (CapabilityInfo, error)
}

func (v *VFS) CapabilitiesForPath(ctx context.Context, path string) (CapabilityInfo, error) {
	path = cleanVirtual(path)
	return v.capabilitiesForPath(ctx, path, v.name, path, false)
}

func (v *VFS) capabilitiesForPath(ctx context.Context, path, mount, displayPath string, mountRoot bool) (CapabilityInfo, error) {
	entry, err := v.Stat(ctx, path)
	if err != nil {
		return CapabilityInfo{}, err
	}
	caps := drive.Capabilities(v.driver)
	writer := hasCapability(caps, drive.CapabilityWriter)
	uploader := hasCapability(caps, drive.CapabilitySourceUploader)
	space := hasCapability(caps, drive.CapabilitySpace)
	targetReadOnly := path == "/" || mountRoot
	return CapabilityInfo{
		Mount:        mount,
		Path:         cleanVirtual(displayPath),
		Root:         false,
		MountRoot:    mountRoot,
		Capabilities: caps,
		CanRead:      !entry.IsDir,
		CanList:      entry.IsDir,
		CanUpload:    entry.IsDir && uploader,
		CanMkdir:     entry.IsDir && writer,
		CanRename:    !targetReadOnly && writer,
		CanMove:      !targetReadOnly && writer,
		CanRemove:    !targetReadOnly && writer,
		CanSpace:     space,
	}, nil
}

func (n *Namespace) CapabilitiesForPath(ctx context.Context, path string) (CapabilityInfo, error) {
	path = cleanVirtual(path)
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return CapabilityInfo{}, err
	}
	if root {
		return CapabilityInfo{Path: "/", Root: true, CanList: true}, nil
	}
	name := cleanMountName(strings.Trim(strings.TrimPrefix(path, "/"), "/"))
	if i := strings.Index(name, "/"); i >= 0 {
		name = name[:i]
	}
	return mount.capabilitiesForPath(ctx, rest, name, path, rest == "/")
}

func hasCapability(caps []drive.Capability, capability drive.Capability) bool {
	return slices.Contains(caps, capability)
}
