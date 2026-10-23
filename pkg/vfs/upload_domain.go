package vfs

import (
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

// uploadDomain is the single owner of the upload pipeline's mutable state.
// VFS owns one domain instead of separately owning the scheduler/service,
// hashes, target index, and engine. Keeping those collaborators together
// makes the pending-generation invariant local to one module.
//
// Service is embedded as a package-internal inspection surface for diagnostics
// and focused tests. Production orchestration belongs on uploadDomain methods;
// callers outside package vfs only see VFS.
type uploadDomain struct {
	*upload.Service

	hashes  *upload.HashTracker
	targets *upload.TargetIndex
	engine  *upload.Engine
}

func newUploadDomain(store *upload.PendingStore, opts Options, done chan struct{}) *uploadDomain {
	hashes := upload.NewHashTracker()
	return &uploadDomain{
		Service: newUploadService(store, opts, done, hashes),
		hashes:  hashes,
		targets: newUploadTargetIndex(),
	}
}

// bind completes construction after VFS has installed the domain. The engine
// adapters need the fully assembled VFS for the cross-domain view/read seams.
func (d *uploadDomain) bind(v *VFS) {
	d.engine = newUploadEngine(v)
}

func (d *uploadDomain) discard(path string) error {
	d.CancelUpload(path)
	if err := d.RemoveUpload(path); err != nil {
		return err
	}
	d.hashes.RemovePath(path)
	return nil
}
