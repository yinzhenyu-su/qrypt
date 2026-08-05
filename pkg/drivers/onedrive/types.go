package onedrive

import (
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type itemResp struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Size           int64  `json:"size"`
	DownloadURL    string `json:"@microsoft.graph.downloadUrl"`
	FileSystemInfo *struct {
		CreatedDateTime      time.Time `json:"createdDateTime"`
		LastModifiedDateTime time.Time `json:"lastModifiedDateTime"`
	} `json:"fileSystemInfo"`
	File *struct {
		MimeType string `json:"mimeType"`
		Hashes   *struct {
			SHA1Hash string `json:"sha1Hash"`
		} `json:"hashes"`
	} `json:"file"`
	Folder *struct {
		ChildCount int64 `json:"childCount"`
	} `json:"folder"`
	ParentReference struct {
		ID      string `json:"id"`
		DriveID string `json:"driveId"`
		Path    string `json:"path"`
	} `json:"parentReference"`
}

func (i itemResp) entry(parentID string) drive.Entry {
	createdAt := time.Time{}
	updatedAt := time.Time{}
	modTime := time.Time{}
	if i.FileSystemInfo != nil {
		createdAt = i.FileSystemInfo.CreatedDateTime
		updatedAt = i.FileSystemInfo.LastModifiedDateTime
		modTime = updatedAt
	}
	if parentID == "" {
		parentID = i.ParentReference.ID
	}
	extra := map[string]any{}
	if i.File != nil && i.File.Hashes != nil && i.File.Hashes.SHA1Hash != "" {
		extra["sha1"] = i.File.Hashes.SHA1Hash
	}
	return drive.Entry{
		ID:        i.ID,
		ParentID:  parentID,
		Name:      i.Name,
		IsDir:     i.Folder != nil,
		Size:      i.Size,
		ModTime:   modTime,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Extra:     extra,
	}
}

type listResp struct {
	Value    []itemResp `json:"value"`
	NextLink string     `json:"@odata.nextLink"`
}

type createUploadSessionResp struct {
	UploadURL          string    `json:"uploadUrl"`
	ExpirationDateTime time.Time `json:"expirationDateTime"`
}

type driveResp struct {
	ID        string `json:"id"`
	DriveType string `json:"driveType"`
	Quota     struct {
		Deleted   int64  `json:"deleted"`
		Remaining int64  `json:"remaining"`
		State     string `json:"state"`
		Total     int64  `json:"total"`
		Used      int64  `json:"used"`
	} `json:"quota"`
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

type onlineTokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ErrorMessage string `json:"text"`
}

type graphErrorResp struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	if e.Code != "" || e.Message != "" {
		return fmt.Sprintf("microsoft graph api error status=%d code=%s message=%s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("microsoft graph api error status=%d", e.Status)
}
