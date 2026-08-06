package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"sort"
	"strings"
)

// MountSpace pairs a mount name with its space usage. Err is set when the
// underlying driver does not support space queries.
type MountSpace struct {
	Name  string
	Space drive.Space
	Err   error
}

func (n *Namespace) PendingUploads() []PendingUpload {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var pending []PendingUpload
	for name, fs := range n.mounts {
		for _, item := range fs.PendingUploads() {
			item.Path = joinVirtual("/"+name, strings.TrimPrefix(item.Path, "/"))
			pending = append(pending, item)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Path < pending[j].Path })
	return pending
}

func (n *Namespace) MountSpaces(ctx context.Context) []MountSpace {
	n.mu.RLock()
	mounts := make([]*VFS, 0, len(n.mounts))
	names := make([]string, 0, len(n.mounts))
	for name, mount := range n.mounts {
		mounts = append(mounts, mount)
		names = append(names, name)
	}
	n.mu.RUnlock()

	results := make([]MountSpace, 0, len(mounts))
	for i, mount := range mounts {
		space, err := mount.Space(ctx)
		results = append(results, MountSpace{Name: names[i], Space: space, Err: err})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results
}

func (n *Namespace) Space(ctx context.Context) (drive.Space, error) {
	n.mu.RLock()
	mounts := make([]*VFS, 0, len(n.mounts))
	for _, mount := range n.mounts {
		mounts = append(mounts, mount)
	}
	n.mu.RUnlock()

	var total drive.Space
	var firstErr error
	for _, mount := range mounts {
		space, err := mount.Space(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		total.Total += space.Total
		total.Free += space.Free
	}
	if total.Total == 0 && total.Free == 0 && firstErr != nil {
		return drive.Space{}, firstErr
	}
	return total, nil
}
