package mobile

import (
	"github.com/yinzhenyu/qrypt/pkg/core"
)

func ListJSON(coreID, path string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	entries, err := s.core.List(ctx, path)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	out := make([]entry, 0, len(entries))
	for _, item := range entries {
		out = append(out, fromDriveEntry(item, core.JoinPath(path, item.Name)))
	}
	return resultJSON(out, nil)
}

func StatJSON(coreID, path string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	item, err := s.core.Stat(ctx, path)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(fromDriveEntry(item, path), nil)
}

func MkdirJSON(coreID, path string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	item, err := s.core.Mkdir(ctx, path)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(fromDriveEntry(item, path), nil)
}

func RenameJSON(coreID, oldPath, newPath string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, s.core.Rename(ctx, oldPath, newPath))
}

func RefreshPathJSON(coreID, path string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	s.core.RefreshPath(path)
	return resultJSON(true, nil)
}

func RemoveJSON(coreID, path string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, s.core.Remove(ctx, path))
}

func CapabilitiesJSON(coreID, path string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	info, err := s.core.Capabilities(ctx, path)
	return resultJSON(info, err)
}

func MountsJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	mounts, err := s.core.Mounts()
	return resultJSON(mounts, err)
}

func FileInfoJSON(coreID, path string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	info, err := s.core.FileInfo(ctx, path)
	return resultJSON(info, err)
}

func ValidateResumeJSON(coreID, path, fileID string, size int64, modTime string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	check, err := s.core.ValidateResume(ctx, path, fileID, size, modTime)
	return resultJSON(check, err)
}
