package p189

import "encoding/xml"

// Folder represents a remote directory.
type Folder struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	LastOpTime string `json:"lastOpTime"`
}

// File represents a remote file.
type File struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	LastOpTime string `json:"lastOpTime"`
	Icon       struct {
		SmallURL string `json:"smallUrl"`
	} `json:"icon"`
	URL string `json:"url"`
}

// ListResp is the response from the list API.
type ListResp struct {
	ResCode    int    `json:"res_code"`
	ResMessage string `json:"res_message"`
	FileListAO struct {
		Count      int      `json:"count"`
		FolderList []Folder `json:"folderList"`
		FileList   []File   `json:"fileList"`
	} `json:"fileListAO"`
}

// DownResp is the download redirect response.
type DownResp struct {
	ResCode         int    `json:"res_code"`
	ResMessage      string `json:"res_message"`
	FileDownloadURL string `json:"downloadUrl"`
}

// UploadUrlsResp contains pre-signed upload URLs.
type UploadUrlsResp struct {
	Code       string         `json:"code"`
	UploadUrls map[string]Part `json:"uploadUrls"`
}

// Part describes one upload part URL.
type Part struct {
	RequestURL    string `json:"requestURL"`
	RequestHeader string `json:"requestHeader"`
}

// UploadCommitResp is the response after committing an upload.
type UploadCommitResp struct {
	ResCode    int    `json:"res_code"`
	ResMessage string `json:"res_message"`
	ID         int64  `json:"id"`
}

type MkdirResp struct {
	XMLName xml.Name `json:"-" xml:"folder"`
	ID      int64    `json:"id" xml:"id"`
}

type CapacityResp struct {
	ResCode    int    `json:"res_code"`
	ResMessage string `json:"res_message"`
	CloudCapacityInfo struct {
		TotalSize int64 `json:"totalSize"`
		FreeSize  int64 `json:"freeSize"`
		UsedSize  int64 `json:"usedSize"`
	} `json:"cloudCapacityInfo"`
}

type xmlCapacity struct {
	XMLName  xml.Name `xml:"capacityInfoVO"`
	Account  string   `xml:"account"`
	CloudCapacityInfo struct {
		TotalSize int64 `xml:"totalSize"`
		FreeSize  int64 `xml:"freeSize"`
		UsedSize  int64 `xml:"usedSize"`
	} `xml:"cloudCapacityInfo"`
}

// SessionResp is the session info response for auth checks.
type SessionResp struct {
	ResCode    int    `json:"res_code"`
	ResMessage string `json:"res_message"`
}
