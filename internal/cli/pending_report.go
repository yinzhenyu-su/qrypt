package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func stagingStatus(item vfs.PendingFile) (string, int64) {
	fi, err := os.Stat(item.LocalPath)
	if err != nil {
		return "missing", 0
	}
	if fi.Size() != item.Size {
		return "size-mismatch", fi.Size()
	}
	return "ok", fi.Size()
}

func formatStagingStatus(status string, size int64) string {
	switch status {
	case "ok":
		return "ok"
	case "missing":
		return "missing"
	case "size-mismatch":
		return fmt.Sprintf("size-mismatch(%d)", size)
	default:
		return status
	}
}

func formatUnixNano(ns int64) string {
	if ns == 0 {
		return "-"
	}
	return time.Unix(0, ns).Format(time.RFC3339)
}
