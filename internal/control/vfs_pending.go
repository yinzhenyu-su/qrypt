package control

import "github.com/yinzhenyu/qrypt/pkg/vfs"

func pendingFiles(fs vfs.FileSystem) []vfs.PendingUpload {
	inspector, ok := fs.(vfs.UploadInspector)
	if !ok {
		return nil
	}
	return inspector.PendingUploads()
}

func pendingCount(fs vfs.FileSystem) int {
	return len(pendingFiles(fs))
}
