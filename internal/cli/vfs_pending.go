package cli

import (
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func pendingFiles(fs any) ([]vfs.PendingUpload, error) {
	return cliruntime.PendingFiles(fs)
}
