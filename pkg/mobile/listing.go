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

type listPageResult struct {
	Entries    []entry `json:"entries,omitempty"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

// ListPageJSON returns a deterministic slice of a directory listing (sorted
// by name) with a name cursor. Pass the previous response's next_cursor to
// fetch the following page; pass "" for the first page. limit <= 0 returns
// the whole listing. Use this for large directories instead of ListJSON so
// each response stays small enough for mobile memory and UI rendering.
func ListPageJSON(coreID, path, cursor string, limit int, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	result, err := s.core.ListPage(ctx, path, cursor, limit)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	out := listPageResult{NextCursor: result.NextCursor}
	for _, item := range result.Entries {
		out.Entries = append(out.Entries, fromDriveEntry(item, core.JoinPath(path, item.Name)))
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
	return resultJSON(map[string]bool{"refreshed": true}, nil)
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
