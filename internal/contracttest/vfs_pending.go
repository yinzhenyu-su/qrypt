package contracttest

import "github.com/yinzhenyu/qrypt/pkg/vfs/vfs"

func pendingFiles(fs any) []vfs.PendingUpload {
	inspector, ok := fs.(vfs.UploadInspector)
	if !ok {
		return nil
	}
	return inspector.PendingUploads()
}

func pendingCount(fs any) int {
	return len(pendingFiles(fs))
}
