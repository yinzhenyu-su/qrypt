package quark

import (
	"encoding/json"
	"regexp"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// conflictSuffix matches auto-rename suffixes like " (2)", " (10)" etc.
// appended by cloud drives (including quark) when uploading a file whose
// name already exists in the target directory.
var conflictSuffix = regexp.MustCompile(`^(.*?)\s*\(\d+\)$`)

type file struct {
	Fid       string      `json:"fid"`
	FileName  string      `json:"file_name"`
	Category  int         `json:"category"`
	Size      json.Number `json:"size"`
	FileSize  json.Number `json:"file_size"`
	CreatedAt int64       `json:"created_at"`
	UpdatedAt int64       `json:"updated_at"`
	File      bool        `json:"file"`
}

func (f file) isDir() bool {
	return !f.File
}

func (f file) int64Size() int64 {
	size, err := f.Size.Int64()
	if err == nil {
		return size
	}
	size, _ = f.FileSize.Int64()
	return size
}

func (f file) modTime() time.Time {
	if f.UpdatedAt > 0 {
		return time.UnixMilli(f.UpdatedAt)
	}
	if f.CreatedAt > 0 {
		return time.UnixMilli(f.CreatedAt)
	}
	return time.Time{}
}

func (f file) entry(parentID string) drive.Entry {
	name := f.FileName
	if m := conflictSuffix.FindStringSubmatch(name); len(m) == 2 {
		name = m[1]
	}
	return drive.Entry{
		ID:       f.Fid,
		ParentID: parentID,
		Name:     name,
		IsDir:    f.isDir(),
		Size:     f.int64Size(),
		ModTime:  f.modTime(),
		Extra:    quarkFileMeta{Category: f.Category},
	}
}

type quarkFileMeta struct {
	Category int
}

type respEnvelope struct {
	Status  int    `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type sortResp struct {
	respEnvelope
	Data struct {
		List []file `json:"list"`
	} `json:"data"`
	Metadata struct {
		Total int `json:"_total"`
	} `json:"metadata"`
}

type downResp struct {
	respEnvelope
	Data []struct {
		DownloadURL string `json:"download_url"`
	} `json:"data"`
}

type upPreResp struct {
	respEnvelope
	Data struct {
		TaskID    string          `json:"task_id"`
		UploadID  string          `json:"upload_id"`
		ObjKey    string          `json:"obj_key"`
		UploadURL string          `json:"upload_url"`
		Fid       string          `json:"fid"`
		Finish    bool            `json:"finish"`
		Bucket    string          `json:"bucket"`
		Callback  json.RawMessage `json:"callback"`
		AuthInfo  string          `json:"auth_info"`
	} `json:"data"`
	Metadata struct {
		PartSize int `json:"part_size"`
	} `json:"metadata"`
}

type upAuthResp struct {
	respEnvelope
	Data struct {
		AuthKey string `json:"auth_key"`
	} `json:"data"`
}

type hashResp struct {
	respEnvelope
	Data struct {
		Finish bool   `json:"finish"`
		Fid    string `json:"fid"`
	} `json:"data"`
}

type createDirResp struct {
	respEnvelope
	Data struct {
		Fid string `json:"fid"`
	} `json:"data"`
}

type cachedURL struct {
	url    string
	expiry time.Time
}
