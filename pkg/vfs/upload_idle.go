package vfs

import (
	"context"
	"sync"
)

// UploadIdleWaiter reports when a filesystem's upload engine has drained: no
// queued, scheduled, claimed or in-flight uploads remain, and no journal
// record is pending. Tests use it instead of polling PendingUploads; both
// *VFS and *Namespace implement it (a namespace waits for every mount).
type UploadIdleWaiter interface {
	WaitUploadsIdle(ctx context.Context) error
}

// WaitUploadsIdle blocks until the upload engine has drained or ctx is done.
// Unlike a polling loop there is no interval and no missed wakeup: the engine
// broadcasts every state transition the answer depends on.
func (v *VFS) WaitUploadsIdle(ctx context.Context) error {
	if v == nil || v.uploads == nil {
		return nil
	}
	return v.uploads.WaitIdle(ctx)
}

// WaitUploadsIdle waits for every mounted filesystem in the namespace to drain
// its upload engine, or for ctx to be done. Mounts are snapshotted first so a
// concurrent AddMount/RemoveMount cannot race the wait.
func (n *Namespace) WaitUploadsIdle(ctx context.Context) error {
	if n == nil {
		return nil
	}
	n.mu.RLock()
	mounts := make([]*VFS, 0, len(n.mounts))
	for _, mount := range n.mounts {
		mounts = append(mounts, mount)
	}
	n.mu.RUnlock()

	errs := make(chan error, len(mounts))
	var wg sync.WaitGroup
	for _, mount := range mounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- mount.WaitUploadsIdle(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
