package cli

import (
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func pendingFiles(fs any) ([]vfs.PendingUpload, error) {
	inspector, ok := fs.(vfs.UploadInspector)
	if !ok {
		return nil, fmt.Errorf("filesystem does not expose upload state")
	}
	return inspector.PendingUploads(), nil
}
