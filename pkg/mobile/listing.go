package mobile

import (
	"context"
	"encoding/json"

	"github.com/yinzhenyu/qrypt/pkg/core"
)

func List(coreID, path string) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	entries, err := s.core.List(context.Background(), path)
	if err != nil {
		return "", wrapError(err)
	}
	out := make([]entry, 0, len(entries))
	for _, item := range entries {
		out = append(out, fromDriveEntry(item, core.JoinPath(path, item.Name)))
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", wrapError(err)
	}
	return string(data), nil
}

func ListJSON(coreID, path string) string {
	raw, err := List(coreID, path)
	if err != nil {
		return resultJSON(nil, err)
	}
	var entries []entry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return resultJSON(nil, err)
	}
	return resultJSON(entries, nil)
}

func Stat(coreID, path string) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	item, err := s.core.Stat(context.Background(), path)
	if err != nil {
		return "", wrapError(err)
	}
	data, err := json.Marshal(fromDriveEntry(item, path))
	if err != nil {
		return "", wrapError(err)
	}
	return string(data), nil
}

func StatJSON(coreID, path string) string {
	raw, err := Stat(coreID, path)
	if err != nil {
		return resultJSON(nil, err)
	}
	var item entry
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return resultJSON(nil, err)
	}
	return resultJSON(item, nil)
}

func MkdirJSON(coreID, path string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	item, err := s.core.Mkdir(ctx, path)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(fromDriveEntry(item, path), nil)
}

func RenameJSON(coreID, oldPath, newPath string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
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

func RemoveJSON(coreID, path string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	return resultJSON(nil, s.core.Remove(ctx, path))
}

func CapabilitiesJSON(coreID, path string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
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

func FileInfoJSON(coreID, path string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	info, err := s.core.FileInfo(context.Background(), path)
	return resultJSON(info, err)
}

func ValidateResumeJSON(coreID, path, id string, size int64, modTime string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	check, err := s.core.ValidateResume(context.Background(), path, id, size, modTime)
	return resultJSON(check, err)
}
