package vfs

import "sort"

type MountInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Encrypted bool   `json:"encrypted"`
}

type MountReporter interface {
	Mounts() []MountInfo
}

func (v *VFS) Mounts() []MountInfo {
	if v == nil {
		return nil
	}
	return []MountInfo{{
		Name:      cleanMountName(v.name),
		Path:      "/",
		Encrypted: v.encrypted,
	}}
}

func (n *Namespace) Mounts() []MountInfo {
	if n == nil {
		return nil
	}
	n.mu.RLock()
	mounts := make([]MountInfo, 0, len(n.mounts))
	for name, fs := range n.mounts {
		mounts = append(mounts, MountInfo{
			Name:      name,
			Path:      "/" + name,
			Encrypted: fs.encrypted,
		})
	}
	n.mu.RUnlock()
	sort.Slice(mounts, func(i, j int) bool {
		return mounts[i].Path < mounts[j].Path
	})
	return mounts
}
