package baidunetdisk

import (
	"strconv"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type file struct {
	Category       int    `json:"category"`
	FsID           int64  `json:"fs_id"`
	Size           int64  `json:"size"`
	Path           string `json:"path"`
	ServerFilename string `json:"server_filename"`
	IsDir          int    `json:"isdir"`
	ServerCtime    int64  `json:"server_ctime"`
	ServerMtime    int64  `json:"server_mtime"`
	Ctime          int64  `json:"ctime"`
	Mtime          int64  `json:"mtime"`
}

func (f file) entry(parentPath string) drive.Entry {
	name := f.ServerFilename
	if name == "" {
		name = baseName(f.Path)
	}
	modUnix := f.ServerMtime
	if modUnix == 0 {
		modUnix = f.Mtime
	}
	var modTime time.Time
	if modUnix > 0 {
		modTime = time.Unix(modUnix, 0)
	}
	return drive.Entry{
		ID:       f.Path,
		ParentID: normalizeDir(parentPath),
		Name:     name,
		IsDir:    f.IsDir == 1,
		Size:     f.Size,
		ModTime:  modTime,
		Extra: map[string]any{
			"fs_id": strconv.FormatInt(f.FsID, 10),
		},
	}
}

type listResp struct {
	Errno  int    `json:"errno"`
	Errmsg string `json:"errmsg"`
	List   []file `json:"list"`
}

type downloadResp struct {
	Errno  int    `json:"errno"`
	Errmsg string `json:"errmsg"`
	List   []struct {
		Dlink string `json:"dlink"`
	} `json:"list"`
}

type createResp struct {
	Errno  int    `json:"errno"`
	Errmsg string `json:"errmsg"`
	FsID   int64  `json:"fs_id"`
	Path   string `json:"path"`
	File   file   `json:"info"`
}

type precreateResp struct {
	Errno      int    `json:"errno"`
	Errmsg     string `json:"errmsg"`
	ReturnType int    `json:"return_type"`
	Path       string `json:"path"`
	UploadID   string `json:"uploadid"`
	BlockList  []int  `json:"block_list"`
	File       file   `json:"info"`
}

type uploadSliceResp struct {
	ErrorCode int    `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
	Errno     int    `json:"errno"`
	Errmsg    string `json:"errmsg"`
}

type quotaResp struct {
	Errno int   `json:"errno"`
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
	ErrorMessage string `json:"text"`
}
