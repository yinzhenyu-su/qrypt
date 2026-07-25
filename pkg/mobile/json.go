package mobile

import (
	"encoding/json"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type envelope struct {
	OK    bool            `json:"ok"`
	Data  any             `json:"data,omitempty"`
	Error *core.ErrorInfo `json:"error,omitempty"`
}

func fromDriveEntry(item drive.Entry, path string) entry {
	out := entry{
		Name:     item.Name,
		Path:     path,
		ID:       item.ID,
		ParentID: item.ParentID,
		IsDir:    item.IsDir,
		Size:     item.Size,
	}
	if !item.ModTime.IsZero() {
		out.ModTime = item.ModTime.Format(time.RFC3339)
	}
	if !item.CreatedAt.IsZero() {
		out.CreatedAt = item.CreatedAt.Format(time.RFC3339)
	}
	if !item.UpdatedAt.IsZero() {
		out.UpdatedAt = item.UpdatedAt.Format(time.RFC3339)
	}
	return out
}

func resultJSON(data any, err error) string {
	env := envelope{OK: err == nil, Data: data}
	if err != nil {
		info := core.ClassifyError(err)
		env.Error = &info
	}
	raw, marshalErr := json.Marshal(env)
	if marshalErr != nil {
		fallback := core.ClassifyError(marshalErr)
		raw, _ = json.Marshal(envelope{OK: false, Error: &fallback})
	}
	return string(raw)
}

func rawResultJSON(raw string, err error) string {
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var data any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return resultJSON(nil, err)
	}
	return resultJSON(data, nil)
}

type classifiedError struct {
	info core.ErrorInfo
}

func (e classifiedError) Error() string {
	if e.info.Code == "" {
		return e.info.Message
	}
	return string(e.info.Code) + ": " + e.info.Message
}

func wrapError(err error) error {
	if err == nil {
		return nil
	}
	return classifiedError{info: core.ClassifyError(err)}
}
