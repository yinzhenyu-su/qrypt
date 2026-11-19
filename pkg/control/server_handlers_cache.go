package control

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/contracttest"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

const (
	defaultCacheFillBytes = 64 << 20
	maxCacheFillBytes     = 1 << 30
	defaultCacheFillBlock = 1 << 20
	minCacheFillBlock     = 4 << 10
	maxCacheFillBlock     = 16 << 20
)

// handleCacheFill warms one file's read cache by reading it through the
// mounted VFS, so the cached chunks land in the same cache the mount serves
// reads from. Reading the file from another process would populate the OS page
// cache instead and leave the probe measuring a cold qrypt cache.
func (s *Server) handleCacheFill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	fs, ok := s.source.(vfs.FileSystem)
	if !ok {
		http.Error(w, "read cache fill unavailable", http.StatusNotImplemented)
		return
	}
	mount := contracttest.NormalizeMountName(r.URL.Query().Get("mount"))
	if mount == "" {
		http.Error(w, "mount query parameter is required", http.StatusBadRequest)
		return
	}
	path := vfstypes.CleanVirtualPath(strings.TrimSpace(r.URL.Query().Get("path")))
	if path == "" || path == "/" {
		http.Error(w, "path query parameter is required", http.StatusBadRequest)
		return
	}
	requested, err := querySize(r, "bytes", defaultCacheFillBytes, maxCacheFillBytes)
	if err != nil {
		http.Error(w, "invalid bytes: "+err.Error(), http.StatusBadRequest)
		return
	}
	blockSize, err := querySize(r, "block_size", defaultCacheFillBlock, maxCacheFillBlock)
	if err != nil {
		http.Error(w, "invalid block_size: "+err.Error(), http.StatusBadRequest)
		return
	}
	if blockSize < minCacheFillBlock {
		blockSize = minCacheFillBlock
	}
	entry, resolved, err := statCacheFillTarget(r, fs, mount, path)
	if err != nil {
		http.Error(w, "resolve fill target: "+err.Error(), http.StatusNotFound)
		return
	}
	if entry.IsDir {
		http.Error(w, "fill target is a directory: "+resolved, http.StatusBadRequest)
		return
	}
	target := requested
	if target <= 0 || target > entry.Size {
		target = entry.Size
	}
	if target > maxCacheFillBytes {
		target = maxCacheFillBytes
	}
	cacheBefore := s.mountReadCacheBytes(mount)
	started := time.Now()
	var readCalls int
	var readBytes int64
	for offset := int64(0); offset < target; {
		size := blockSize
		if remaining := target - offset; remaining < size {
			size = remaining
		}
		reader, err := fs.Read(r.Context(), resolved, offset, size)
		if err != nil {
			http.Error(w, fmt.Sprintf("read %s at offset %d: %v", resolved, offset, err), http.StatusBadGateway)
			return
		}
		copied, copyErr := io.Copy(io.Discard, reader)
		closeErr := reader.Close()
		if copyErr != nil {
			http.Error(w, fmt.Sprintf("read %s at offset %d: %v", resolved, offset, copyErr), http.StatusBadGateway)
			return
		}
		if closeErr != nil {
			http.Error(w, fmt.Sprintf("close %s at offset %d: %v", resolved, offset, closeErr), http.StatusBadGateway)
			return
		}
		readCalls++
		readBytes += copied
		offset += copied
		if copied == 0 {
			break
		}
	}
	writeJSON(w, DebugCacheFillResponse{
		SchemaVersion:    diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:      time.Now(),
		Mount:            mount,
		Path:             resolved,
		FileSize:         entry.Size,
		RequestedBytes:   requested,
		Bytes:            readBytes,
		BlockSize:        blockSize,
		ReadCalls:        readCalls,
		DurationMS:       time.Since(started).Milliseconds(),
		CacheBytesBefore: cacheBefore,
		CacheBytesAfter:  s.mountReadCacheBytes(mount),
	})
}

func statCacheFillTarget(r *http.Request, fs vfs.FileSystem, mount, path string) (drive.Entry, string, error) {
	entry, err := fs.Stat(r.Context(), path)
	if err == nil {
		return entry, path, nil
	}
	// Callers may pass either the virtual path (/mount/file) or the path
	// relative to the mount root (/file); try the mount-prefixed form.
	if prefixed := "/" + mount + path; !strings.HasPrefix(prefixed, "//") {
		if prefixedEntry, prefixedErr := fs.Stat(r.Context(), prefixed); prefixedErr == nil {
			return prefixedEntry, prefixed, nil
		}
	}
	return drive.Entry{}, "", err
}

func querySize(r *http.Request, name string, fallback, maxBytes int64) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, fmt.Errorf("must not be negative")
	}
	if maxBytes > 0 && value > maxBytes {
		return 0, fmt.Errorf("must not exceed %d", maxBytes)
	}
	return value, nil
}

func (s *Server) mountReadCacheBytes(mount string) int64 {
	for _, state := range s.source.DebugSnapshot().Mounts {
		if strings.EqualFold(state.Identity.Name, mount) {
			return state.Cache.Bytes
		}
	}
	return 0
}
