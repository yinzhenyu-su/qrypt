package vfs

import "time"

type uploadStoreAdapter struct {
	store *uploadStore
}

func newUploadStoreAdapter(store *uploadStore) uploadStoreAdapter {
	return uploadStoreAdapter{store: store}
}

func (a uploadStoreAdapter) UploadByPath(path string) (PendingUpload, bool) {
	return a.store.UploadByPath(path)
}

func (a uploadStoreAdapter) RemoveStagingIfUnreferenced(localPath string) {
	a.store.removeStagingIfUnreferenced(localPath)
}

func (a uploadStoreAdapter) RecordPermanentFailureIfUnchanged(pending PendingUpload, err error) (PendingUpload, bool, error) {
	return a.store.RecordUploadPermanentFailureIfUnchanged(pending, err)
}

func (a uploadStoreAdapter) RecordFailureIfUnchanged(pending PendingUpload, err error, retryDelay time.Duration) (PendingUpload, bool, error) {
	return a.store.RecordUploadFailureIfUnchanged(pending, err, retryDelay)
}

func (a uploadStoreAdapter) RecordReplacementIfUnchanged(pending PendingUpload, replacement UploadReplacement) (PendingUpload, bool, error) {
	return a.store.RecordUploadReplacementIfUnchanged(pending, replacement)
}

func (a uploadStoreAdapter) RemoveIfUnchanged(pending PendingUpload) (bool, error) {
	return a.store.RemoveUploadIfUnchanged(pending)
}

func (a uploadStoreAdapter) RemoveStaging(localPath string) error {
	return a.store.staging.remove(localPath)
}
