package upload

import (
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// StagingFID derives the stable staging-file ID for a virtual path.
func StagingFID(path string) string {
	path = strings.Trim(vfstypes.CleanVirtualPath(path), "/")
	if path == "" {
		return "root"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	return replacer.Replace(path)
}

// NewStagingFID returns a unique staging-file ID for a virtual path.
func NewStagingFID(path string) string {
	base := StagingFID(path)
	return base + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// CopyStagingContent copies the source staging file into dst, returning
// bytes written (0 with nil error when the source does not exist).
func CopyStagingContent(srcPath, dstPath string) (int64, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer src.Close()
	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return written, nil
}
