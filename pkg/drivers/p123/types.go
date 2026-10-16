// Package p123 implements the 123pan (123 云盘) personal-cloud-drive backend.
//
// The driver speaks 123pan's web-client API: username/password login returns a
// bearer token, every request carries a CRC32 signature in its query string,
// and downloads resolve through a signed CDN URL. Uploads use S3 presigned
// part URLs (with MD5-based instant upload for content 123pan already holds).
package p123

import (
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	fileTypeFile = 0
	fileTypeDir  = 1

	apiCodeOK             = 0
	apiCodeNotFound       = 2
	apiCodeAuth           = 401
	apiCodeInvalidRequest = 400
	apiCodeRateLimit      = 429
	apiCodeNoFile         = 5056
	apiCodeDuplicate      = 5060

	loginCodeOK = 200
)

// apiEnvelope is the status prefix shared by every 123pan JSON response.
type apiEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// remoteFile mirrors one entry returned by /file/list/new and the file_info
// payloads of mkdir/rename/move/upload responses. 123pan field names are
// capitalized; the JSON tags keep the mapping in one place.
type remoteFile struct {
	FileID      int64  `json:"FileId"`
	FileName    string `json:"FileName"`
	Type        int    `json:"Type"`
	Size        int64  `json:"Size"`
	Etag        string `json:"Etag"`
	S3KeyFlag   string `json:"S3KeyFlag"`
	ParentID    int64  `json:"ParentFileId"`
	StorageNode string `json:"StorageNode"`
	CreateAt    string `json:"CreateAt"`
	UpdateAt    string `json:"UpdateAt"`
}

func (f remoteFile) isDir() bool { return f.Type == fileTypeDir }

func (f remoteFile) times() (created, updated time.Time) {
	return parseAPITime(f.CreateAt), parseAPITime(f.UpdateAt)
}

func (f remoteFile) modTime() time.Time {
	created, updated := f.times()
	if !updated.IsZero() {
		return updated
	}
	return created
}

func (f remoteFile) entry(parentID string) drive.Entry {
	created, updated := f.times()
	return drive.Entry{
		ID:        strconv.FormatInt(f.FileID, 10),
		ParentID:  parentID,
		Name:      f.FileName,
		IsDir:     f.isDir(),
		Size:      f.Size,
		ModTime:   f.modTime(),
		CreatedAt: created,
		UpdatedAt: updated,
		Extra:     fileMetaFromFile(f, parentID),
	}
}

// fileMeta is the provider metadata Read and Remove need beyond a plain
// drive.Entry. It rides on Entry.Extra so crypt's EntryExtraWrapper can unwrap
// it with drive.RawEntryExtra.
type fileMeta struct {
	FileID    int64
	Name      string
	Type      int
	Size      int64
	Etag      string
	S3KeyFlag string
	ParentID  int64
}

func fileMetaFromFile(f remoteFile, parentID string) fileMeta {
	meta := fileMeta{
		FileID:    f.FileID,
		Name:      f.FileName,
		Type:      f.Type,
		Size:      f.Size,
		Etag:      f.Etag,
		S3KeyFlag: f.S3KeyFlag,
	}
	if id, err := strconv.ParseInt(parentID, 10, 64); err == nil {
		meta.ParentID = id
	} else {
		meta.ParentID = f.ParentID
	}
	return meta
}

// metaFromEntry unwraps the provider metadata a listing attached to entry.
func metaFromEntry(entry drive.Entry) (fileMeta, bool) {
	if meta, ok := drive.RawEntryExtra(entry.Extra).(fileMeta); ok {
		return meta, true
	}
	return fileMeta{}, false
}

// fallbackMeta synthesizes metadata from the public entry fields. It is used
// when a caller hands us an entry that did not come from a listing.
func fallbackMeta(entry drive.Entry) fileMeta {
	id, _ := strconv.ParseInt(entry.ID, 10, 64)
	parentID, _ := strconv.ParseInt(entry.ParentID, 10, 64)
	typ := fileTypeFile
	if entry.IsDir {
		typ = fileTypeDir
	}
	return fileMeta{
		FileID:   id,
		Name:     entry.Name,
		Type:     typ,
		Size:     entry.Size,
		ParentID: parentID,
	}
}

type loginResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Token  string `json:"token"`
		Expire string `json:"expire"`
	} `json:"data"`
}

type listResp struct {
	apiEnvelope
	Data struct {
		Next     string       `json:"Next"`
		Total    int          `json:"Total"`
		InfoList []remoteFile `json:"InfoList"`
	} `json:"data"`
}

type userInfoResp struct {
	apiEnvelope
	Data struct {
		UID            int64  `json:"UID"`
		Nickname       string `json:"Nickname"`
		SpaceUsed      int64  `json:"SpaceUsed"`
		SpacePermanent int64  `json:"SpacePermanent"`
		SpaceTemp      int64  `json:"SpaceTemp"`
	} `json:"data"`
}

type uploadRequestResp struct {
	apiEnvelope
	Data struct {
		AccessKeyID     string      `json:"AccessKeyId"`
		SecretAccessKey string      `json:"SecretAccessKey"`
		SessionToken    string      `json:"SessionToken"`
		Bucket          string      `json:"Bucket"`
		Key             string      `json:"Key"`
		FileID          int64       `json:"FileId"`
		Reuse           bool        `json:"Reuse"`
		EndPoint        string      `json:"EndPoint"`
		StorageNode     string      `json:"StorageNode"`
		UploadID        string      `json:"UploadId"`
		Info            *remoteFile `json:"Info"`
	} `json:"data"`
}

type presignedResp struct {
	apiEnvelope
	Data struct {
		PreSignedUrls map[string]string `json:"presignedUrls"`
	} `json:"data"`
}

type uploadCompleteResp struct {
	apiEnvelope
	Data struct {
		FileInfo *remoteFile `json:"file_info"`
	} `json:"data"`
}

type downloadInfoResp struct {
	apiEnvelope
	Data struct {
		DownloadURL string `json:"DownloadUrl"`
	} `json:"data"`
}

type renameResp struct {
	apiEnvelope
	Data struct {
		Info *remoteFile `json:"Info"`
	} `json:"data"`
}

type moveResp struct {
	apiEnvelope
	Data struct {
		Info []remoteFile `json:"Info"`
	} `json:"data"`
}

// parseAPITime accepts both RFC3339 timestamps (list/upload responses) and the
// "2006-01-02 15:04:05" form some endpoints (move) return.
func parseAPITime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}
