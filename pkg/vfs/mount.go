package vfs

import (
	"sort"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

type MountInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Encrypted bool   `json:"encrypted"`
	// State is "failed" when a configured mount could not be initialized and
	// was excluded from the namespace; successfully mounted mounts leave it
	// empty. Error carries the initialization failure for failed mounts.
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
}

// MountFailure records a configured mount that could not be initialized when
// the namespace was built. The namespace still opens with the mounts that
// succeeded; failures are surfaced through MountReporter so clients (mobile
// MountsJSON, CLI) can tell the user which drives are absent and why.
type MountFailure struct {
	Name string
	Err  error
}

type MountReporter interface {
	Mounts() []MountInfo
}

func (v *VFS) Mounts() []MountInfo {
	if v == nil {
		return nil
	}
	return []MountInfo{{
		Name:      vfstypes.CleanMountName(v.name),
		Path:      "/",
		Encrypted: v.encrypted,
	}}
}

func (n *Namespace) Mounts() []MountInfo {
	if n == nil {
		return nil
	}
	n.mu.RLock()
	mounts := make([]MountInfo, 0, len(n.mounts)+len(n.failures))
	for name, fs := range n.mounts {
		mounts = append(mounts, MountInfo{
			Name:      name,
			Path:      "/" + name,
			Encrypted: fs.encrypted,
		})
	}
	failures := make([]MountInfo, 0, len(n.failures))
	for _, failure := range n.failures {
		failures = append(failures, MountInfo{
			Name:  failure.Name,
			State: "failed",
			Error: failure.Err.Error(),
		})
	}
	n.mu.RUnlock()
	sort.Slice(mounts, func(i, j int) bool {
		return mounts[i].Path < mounts[j].Path
	})
	sort.Slice(failures, func(i, j int) bool {
		return failures[i].Name < failures[j].Name
	})
	return append(mounts, failures...)
}

// SetMountFailures records configured mounts that failed to initialize when
// the namespace was built. It is called once by the core builder after the
// surviving mounts are assembled; failed mounts stay visible to MountsJSON
// so clients can report which drives are absent and why.
func (n *Namespace) SetMountFailures(failures []MountFailure) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.failures = append([]MountFailure(nil), failures...)
}
