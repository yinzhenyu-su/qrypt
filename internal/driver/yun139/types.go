package yun139

import (
	"regexp"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// cloudRenameSuffix matches auto-rename suffixes added by 139 cloud drive
// when a file with the same name already exists: _YYYYMMDD_HHMMSS.
var cloudRenameSuffix = regexp.MustCompile(`_\d{8}_\d{6}$`)

type baseResp struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type personalListResp struct {
	baseResp
	Data struct {
		NextPageCursor string         `json:"nextPageCursor"`
		Items          []personalItem `json:"items"`
	} `json:"data"`
}

type personalItem struct {
	FileId     string       `json:"fileId"`
	Name       string       `json:"name"`
	Type       string       `json:"type"`
	Size       int64        `json:"size"`
	CreatedAt  string       `json:"createdAt"`
	UpdatedAt  string       `json:"updatedAt"`
	Thumbnails []thumbEntry `json:"thumbnailUrlList"`
}

type thumbEntry struct {
	Style string `json:"style"`
	Url   string `json:"url"`
}

func personalParseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02T15:04:05.999-07:00", s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func toEntry(item personalItem) drive.Entry {
	name := cloudRenameSuffix.ReplaceAllString(item.Name, "")
	modTime := personalParseTime(item.UpdatedAt)
	if modTime.IsZero() {
		modTime = personalParseTime(item.CreatedAt)
	}
	return drive.Entry{
		ID:      item.FileId,
		Name:    name,
		IsDir:   item.Type == "folder",
		Size:    item.Size,
		ModTime: modTime,
	}
}

func toEntries(items []personalItem) []drive.Entry {
	entries := make([]drive.Entry, len(items))
	for i := range items {
		entries[i] = toEntry(items[i])
	}
	return entries
}

type createResp struct {
	baseResp
	Data struct {
		FileId string `json:"fileId"`
		Name   string `json:"name"`
		Type   string `json:"type"`
	} `json:"data"`
}

type downloadResp struct {
	baseResp
	Data struct {
		Url    string `json:"url"`
		CdnUrl string `json:"cdnUrl"`
	} `json:"data"`
}

type personalUploadResp struct {
	baseResp
	Data struct {
		FileId      string             `json:"fileId"`
		FileName    string             `json:"fileName"`
		Exist       bool               `json:"exist"`
		RapidUpload bool               `json:"rapidUpload"`
		UploadId    string             `json:"uploadId"`
		PartInfos   []personalPartInfo `json:"partInfos"`
	} `json:"data"`
}

type personalUploadUrlResp struct {
	baseResp
	Data struct {
		PartInfos []personalPartInfo `json:"partInfos"`
	} `json:"data"`
}

type personalPartInfo struct {
	PartNumber int    `json:"partNumber"`
	UploadUrl  string `json:"uploadUrl"`
}

type quotaDetailResp struct {
	baseResp
	Data struct {
		FreeDiskSize int64 `json:"freeDiskSize"`
		DiskSize     int64 `json:"diskSize"`
	} `json:"data"`
}
