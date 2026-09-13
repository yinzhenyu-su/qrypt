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

// StagingCopyChunkSize is the read size used when copying staged content.
// Callers hash the chunks as they are copied, so the bytes necessarily pass
// through userspace and the kernel copy fast path io.Copy can pick does not
// apply.
const StagingCopyChunkSize = 256 * 1024

// CopyStagingContent copies the source staging file into dst, returning
// bytes written (0 with nil error when the source does not exist). onChunk,
// when non-nil, receives every copied chunk with its destination offset so
// callers can derive hashes without re-reading the file; the chunk slice is
// only valid for the duration of the call.
func CopyStagingContent(srcPath, dstPath string, onChunk func(chunk []byte, off int64)) (int64, error) {
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
	written, copyErr := CopyStagingStream(dst, src, onChunk)
	closeErr := dst.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return written, nil
}

// CopyStagingStream copies src into dst in StagingCopyChunkSize steps,
// reporting each chunk with its destination offset as it lands.
func CopyStagingStream(dst io.Writer, src io.Reader, onChunk func(chunk []byte, off int64)) (int64, error) {
	buf := make([]byte, StagingCopyChunkSize)
	var written int64
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			copied, writeErr := dst.Write(buf[:n])
			if writeErr != nil {
				return written, writeErr
			}
			if copied != n {
				return written, io.ErrShortWrite
			}
			if onChunk != nil {
				onChunk(buf[:n], written)
			}
			written += int64(n)
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}
