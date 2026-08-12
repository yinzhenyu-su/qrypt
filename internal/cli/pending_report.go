package cli

import (
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func stagingStatus(item vfs.PendingUpload) (string, int64) {
	return cliruntime.StagingStatus(item)
}

func formatStagingStatus(status string, size int64) string {
	return cliruntime.FormatStagingStatus(status, size)
}

func formatUnixNano(ns int64) string {
	return cliruntime.FormatUnixNano(ns)
}
