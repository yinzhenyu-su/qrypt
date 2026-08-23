package mobile

import (
	"fmt"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/media"
	vfsread "github.com/yinzhenyu/qrypt/pkg/vfs/read"
)

func ProbeMP4JSON(coreID, path string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	probe, err := withCore(s, func(c *core.Core) (media.MP4Probe, error) { return c.ProbeMP4(ctx, path) })
	return resultJSON(probe, err)
}

type virtualOpenResult struct {
	Handle string                `json:"handle"`
	Info   media.VirtualFileInfo `json:"info"`
}

func openVirtualFile(coreID, path, mode string, deadlineMS int) (virtualOpenResult, error) {
	s, err := getSession(coreID)
	if err != nil {
		return virtualOpenResult{}, wrapError(err)
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	file, err := withCore(s, func(c *core.Core) (media.VirtualFile, error) { return c.OpenVirtualFile(ctx, path, mode) })
	if err != nil {
		return virtualOpenResult{}, wrapError(err)
	}
	id, err := newID()
	if err != nil {
		_ = file.Close()
		return virtualOpenResult{}, wrapError(err)
	}
	registry.mu.Lock()
	registry.virtuals[id] = &virtualHandle{coreID: coreID, file: file, readSession: nextReadSessionID()}
	registry.mu.Unlock()
	return virtualOpenResult{Handle: id, Info: file.Info()}, nil
}

func OpenVirtualFileJSON(coreID, path, mode string, deadlineMS int) string {
	data, err := openVirtualFile(coreID, path, mode, deadlineMS)
	return resultJSON(data, err)
}

func ReadVirtualFileAtInto(handleID string, offset int64, dst []byte, deadlineMS int) (int, error) {
	if offset < 0 {
		return 0, wrapError(fmt.Errorf("mobile: offset must be non-negative"))
	}
	if len(dst) == 0 {
		return 0, nil
	}
	handle, err := getVirtualFile(handleID)
	if err != nil {
		return 0, wrapError(err)
	}
	ctx, done, concurrent, accessID, _, err := handle.reads.begin(deadlineMS)
	if err != nil {
		return 0, wrapError(err)
	}
	defer done()
	ctx = vfsread.WithAccessHint(ctx, vfsread.AccessHint{
		SessionID:  handle.readSession,
		RequestID:  accessID,
		Concurrent: concurrent,
	})
	info := handle.file.Info()
	started := time.Now()
	n, err := handle.file.ReadAtInto(ctx, offset, dst)
	logVirtualRead(handleID, handle.file, info, offset, len(dst), n, time.Since(started), err)
	if err != nil {
		return n, wrapError(err)
	}
	return n, nil
}

// CancelVirtualReadJSON aborts any in-flight virtual reads on the handle. The
// handle remains usable; future reads are unaffected.
func CancelVirtualReadJSON(handleID string) string {
	handle, err := getVirtualFile(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	handle.reads.cancelAll()
	return resultJSON(nil, nil)
}

func logVirtualRead(handleID string, file media.VirtualFile, info media.VirtualFileInfo, offset int64, requested, bytes int, dur time.Duration, err error) {
	if err != nil {
		mappings := file.ReadMappings(offset, requested)
		logging.L.Warnf("[MOBILE] virtual read handle=%q mode=%s transformed=%t offset=%d requested=%d bytes=%d dur=%s mappings=%s err=%v",
			handleID, info.Mode, info.Transformed, offset, requested, bytes, dur, formatReadMappings(mappings), err)
		return
	}
	logging.L.DebugfEveryFunc(virtualReadLogKey(handleID, info), time.Second, func(int) string {
		mappings := file.ReadMappings(offset, requested)
		return fmt.Sprintf("[MOBILE] virtual read handle=%q mode=%s transformed=%t offset=%d requested=%d bytes=%d dur=%s mappings=%s err=%v",
			handleID, info.Mode, info.Transformed, offset, requested, bytes, dur, formatReadMappings(mappings), err)
	})
}

func virtualReadLogKey(handleID string, info media.VirtualFileInfo) string {
	return "mobile.virtual_read." + handleID + "." + info.Mode
}

func formatReadMappings(mappings []media.VirtualReadMapping) string {
	if len(mappings) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(mappings))
	for _, item := range mappings {
		parts = append(parts, fmt.Sprintf("%d+%d:%s@%d", item.VirtualOffset, item.Length, item.Source, item.SourceOffset))
	}
	return strings.Join(parts, ",")
}

func closeVirtualFile(handleID string) error {
	registry.mu.Lock()
	handle, ok := registry.virtuals[handleID]
	if ok {
		delete(registry.virtuals, handleID)
	}
	registry.mu.Unlock()
	if !ok {
		return wrapError(fmt.Errorf("mobile: unknown virtual file handle %q", handleID))
	}
	handle.reads.cancelAll()
	s, err := getSession(handle.coreID)
	if err == nil {
		_ = withCoreErr(s, func(c *core.Core) error {
			c.ReleaseReadSession(handle.readSession)
			return nil
		})
	}
	return wrapError(handle.file.Close())
}

func CloseVirtualFileJSON(handleID string) string {
	return resultJSON(nil, closeVirtualFile(handleID))
}
