package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"sort"
	"strings"
	"sync"
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

	results := make([]MountSpace, len(mounts))
	var wg sync.WaitGroup
	for i, mount := range mounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			space, err := mount.Space(ctx)
			results[i] = MountSpace{Name: names[i], Space: space, Err: err}
		}()
	}
	wg.Wait()
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

	results := make([]MountSpace, len(mounts))
	var wg sync.WaitGroup
	for i, mount := range mounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].Space, results[i].Err = mount.Space(ctx)
		}()
	}
	wg.Wait()

	var total drive.Space
	var firstErr error
	for _, result := range results {
		if result.Err != nil {
			if firstErr == nil {
				firstErr = result.Err
			}
			continue
		}
		total.Total += result.Space.Total
		total.Free += result.Space.Free
	}
	if total.Total == 0 && total.Free == 0 && firstErr != nil {
		return drive.Space{}, firstErr
	}
	return total, nil
}
