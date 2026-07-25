package mobile

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/media"
)

func ProbeMP4JSON(coreID, path string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	probe, err := s.core.ProbeMP4(ctx, path)
	return resultJSON(probe, err)
}

type virtualOpenResult struct {
	Handle string                `json:"handle"`
	Info   media.VirtualFileInfo `json:"info"`
}

func OpenVirtualFile(coreID, path, mode string, timeoutMS int) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	file, err := s.core.OpenVirtualFile(ctx, path, mode)
	if err != nil {
		return "", wrapError(err)
	}
	id, err := newID()
	if err != nil {
		_ = file.Close()
		return "", wrapError(err)
	}
	registry.mu.Lock()
	registry.virtuals[id] = &virtualHandle{coreID: coreID, file: file}
	registry.mu.Unlock()
	data, err := json.Marshal(virtualOpenResult{Handle: id, Info: file.Info()})
	if err != nil {
		_ = CloseVirtualFile(id)
		return "", wrapError(err)
	}
	return string(data), nil
}

func OpenVirtualFileJSON(coreID, path, mode string, timeoutMS int) string {
	raw, err := OpenVirtualFile(coreID, path, mode, timeoutMS)
	if err != nil {
		return resultJSON(nil, err)
	}
	var data virtualOpenResult
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return resultJSON(nil, err)
	}
	return resultJSON(data, nil)
}

func ReadVirtualFileAt(handleID string, offset int64, length int, timeoutMS int) ([]byte, error) {
	handle, err := getVirtualFile(handleID)
	if err != nil {
		return nil, wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	info := handle.file.Info()
	started := time.Now()
	data, err := handle.file.ReadAt(ctx, offset, length)
	logVirtualRead(handleID, handle.file, info, offset, length, len(data), time.Since(started), err)
	if err != nil {
		return nil, wrapError(err)
	}
	return data, nil
}

func ReadVirtualFileAtInto(handleID string, offset int64, dst []byte, timeoutMS int) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	handle, err := getVirtualFile(handleID)
	if err != nil {
		return 0, wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	info := handle.file.Info()
	started := time.Now()
	n, err := handle.file.ReadAtInto(ctx, offset, dst)
	logVirtualRead(handleID, handle.file, info, offset, len(dst), n, time.Since(started), err)
	if err != nil {
		return n, wrapError(err)
	}
	return n, nil
}

func ReadVirtualFileAtJSON(handleID string, offset int64, length int, timeoutMS int) string {
	data, err := ReadVirtualFileAt(handleID, offset, length, timeoutMS)
	return resultJSON(data, err)
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

func CloseVirtualFile(handleID string) error {
	registry.mu.Lock()
	handle, ok := registry.virtuals[handleID]
	if ok {
		delete(registry.virtuals, handleID)
	}
	registry.mu.Unlock()
	if !ok {
		return wrapError(fmt.Errorf("mobile: unknown virtual file handle %q", handleID))
	}
	return wrapError(handle.file.Close())
}

func CloseVirtualFileJSON(handleID string) string {
	return resultJSON(nil, CloseVirtualFile(handleID))
}
