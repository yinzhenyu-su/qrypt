package vfs

import (
	"sync"
)

type uploadAdmission struct {
	mu          sync.Mutex
	activeSmall int
	activeLarge bool
}

func (a *uploadAdmission) tryAcquire(p PendingUpload, workers int) bool {
	if workers <= 0 {
		workers = 1
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if isLargeUpload(p) {
		if a.activeLarge || a.activeSmall > 0 {
			return false
		}
		a.activeLarge = true
		return true
	}
	if a.activeLarge || a.activeSmall >= workers {
		return false
	}
	a.activeSmall++
	return true
}
func (a *uploadAdmission) release(p PendingUpload) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if isLargeUpload(p) {
		a.activeLarge = false
		return
	}
	if a.activeSmall > 0 {
		a.activeSmall--
	}
}
func isLargeUpload(p PendingUpload) bool {
	return p.Size >= largeUploadQuietThreshold
}
