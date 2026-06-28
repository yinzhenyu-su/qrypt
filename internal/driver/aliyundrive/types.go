package aliyundrive

import (
	"encoding/json"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type userResp struct {
	DefaultDriveID string `json:"default_drive_id"`
	UserID         string `json:"user_id"`
}

type listResp struct {
	Items      []file `json:"items"`
	NextMarker string `json:"next_marker"`
}

type file struct {
	DriveID      string     `json:"drive_id"`
	FileID       string     `json:"file_id"`
	ParentFileID string     `json:"parent_file_id"`
	Type         string     `json:"type"`
	Name         string     `json:"name"`
	Size         int64      `json:"size"`
	CreatedAt    *time.Time `json:"created_at"`
	UpdatedAt    *time.Time `json:"updated_at"`
}

func (f file) entry(parentID string) drive.Entry {
	modTime := time.Time{}
	if f.UpdatedAt != nil {
		modTime = *f.UpdatedAt
	} else if f.CreatedAt != nil {
		modTime = *f.CreatedAt
	}
	if parentID == "" {
		parentID = f.ParentFileID
	}
	return drive.Entry{
		ID:       f.FileID,
		ParentID: parentID,
		Name:     f.Name,
		IsDir:    f.Type == "folder",
		Size:     f.Size,
		ModTime:  modTime,
		Extra:    f,
	}
}

type createResp struct {
	FileID       string           `json:"file_id"`
	Name         string           `json:"name"`
	Type         string           `json:"type"`
	Size         int64            `json:"size"`
	CreatedAt    *time.Time       `json:"created_at"`
	UpdatedAt    *time.Time       `json:"updated_at"`
	UploadID     string           `json:"upload_id"`
	RapidUpload  bool             `json:"rapid_upload"`
	PartInfoList []uploadPartInfo `json:"part_info_list"`
}

type uploadPartInfo struct {
	PartNumber int    `json:"part_number"`
	UploadURL  string `json:"upload_url"`
}

type downloadURLResp struct {
	URL string `json:"url"`
}

type completeResp struct {
	FileID    string     `json:"file_id"`
	Name      string     `json:"name"`
	Type      string     `json:"type"`
	Size      int64      `json:"size"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

type capacityResp struct {
	DriveUsedSize  int64 `json:"drive_used_size"`
	DriveTotalSize int64 `json:"drive_total_size"`
}

type batchResp struct {
	Responses []batchItemResp `json:"responses"`
}

type batchItemResp struct {
	ID     string          `json:"id"`
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}
