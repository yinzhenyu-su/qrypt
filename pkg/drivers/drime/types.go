package drime

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	itemTypeFolder = "folder"
	dirSizeZero    = 0
)

// apiEntry mirrors one Drime FileEntry. IDs are numeric; the encoded hash is a
// separate URL identity used by download and share URLs.
type apiEntry struct {
	ID                 int64           `json:"id"`
	Name               string          `json:"name"`
	Type               string          `json:"type"`
	FileSize           int64           `json:"file_size"`
	ParentID           *int64          `json:"parent_id"`
	WorkspaceID        int64           `json:"workspace_id"`
	Hash               string          `json:"hash"`
	FileHash           *string         `json:"file_hash"`
	ClientLastModified *int64          `json:"client_last_modified"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	URL                string          `json:"url"`
	Mime               string          `json:"mime"`
	Extension          *string         `json:"extension"`
	Permissions        map[string]bool `json:"permissions"`
}

type apiListing struct {
	Data        []apiEntry `json:"data"`
	CurrentPage int        `json:"current_page"`
	LastPage    int        `json:"last_page"`
	PerPage     int        `json:"per_page"`
	Total       int        `json:"total"`
	Folder      *apiEntry  `json:"folder"`
}

type apiErrorBody struct {
	Status          string              `json:"status"`
	Message         string              `json:"message"`
	Errors          map[string][]string `json:"errors"`
	PasswordInvalid bool                `json:"passwordInvalid"`
}

type apiUploadResponse struct {
	Status    string   `json:"status"`
	FileEntry apiEntry `json:"fileEntry"`
}

type apiFolderResponse struct {
	Status string   `json:"status"`
	Folder apiEntry `json:"folder"`
}

type apiMoveResponse struct {
	Status      string     `json:"status"`
	Entries     []apiEntry `json:"entries"`
	Destination *apiEntry  `json:"destination"`
}

type apiDeleteResponse struct {
	Status  string            `json:"status"`
	Marked  int               `json:"marked"`
	Bytes   int64             `json:"bytes"`
	Errors  map[string]string `json:"errors"`
	Message string            `json:"message"`
}

type apiHashCheckResponse struct {
	Status string    `json:"status"`
	Exists bool      `json:"exists"`
	Entry  *apiEntry `json:"entry"`
}

type apiVerifyResponse struct {
	Status     string `json:"status"`
	Verified   bool   `json:"verified"`
	ServerHash string `json:"serverHash"`
	Reason     string `json:"reason"`
}

type apiPresignResponse struct {
	Status string `json:"status"`
	URL    string `json:"url"`
	Key    string `json:"key"`
	ACL    string `json:"acl"`
}

type apiSpaceResponse struct {
	Status    string `json:"status"`
	Used      int64  `json:"used"`
	Available int64  `json:"available"`
}

type apiUserResponse struct {
	Status string `json:"status"`
	User   struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

type apiMultipartCreateResponse struct {
	Status   string `json:"status"`
	Key      string `json:"key"`
	UploadID string `json:"uploadId"`
	ACL      string `json:"acl"`
}

type apiSignedPart struct {
	PartNumber int    `json:"partNumber"`
	URL        string `json:"url"`
}

type apiSignPartsResponse struct {
	Status string          `json:"status"`
	URLs   []apiSignedPart `json:"urls"`
}

type apiUploadedPart struct {
	PartNumber int    `json:"PartNumber"`
	ETag       string `json:"ETag"`
	Size       int64  `json:"Size"`
}

type apiUploadedPartsResponse struct {
	Status string            `json:"status"`
	Parts  []apiUploadedPart `json:"parts"`
}

type apiCompleteResponse struct {
	Status   string `json:"status"`
	Location string `json:"location"`
}

type apiShareLink struct {
	ID            int64     `json:"id"`
	Hash          string    `json:"hash"`
	UserID        int64     `json:"user_id"`
	EntryID       int64     `json:"entry_id"`
	AllowEdit     bool      `json:"allow_edit"`
	AllowDownload bool      `json:"allow_download"`
	ExpiresAt     *string   `json:"expires_at"`
	RevokedAt     *string   `json:"revoked_at"`
	CreatedAt     string    `json:"created_at"`
	UpdatedAt     string    `json:"updated_at"`
	Password      *string   `json:"password"`
	RequireEmail  bool      `json:"require_email"`
	BrandingTitle *string   `json:"branding_title"`
	BrandingDesc  *string   `json:"branding_description"`
	Entry         *apiEntry `json:"entry"`
}

type apiImportResponse struct {
	Status    string     `json:"status"`
	Entry     *apiEntry  `json:"entry"`
	FileEntry *apiEntry  `json:"fileEntry"`
	Entries   []apiEntry `json:"entries"`
	Message   string     `json:"message"`
}

type apiShareLinkResponse struct {
	Status          string        `json:"status"`
	Link            *apiShareLink `json:"link"`
	FolderChildren  *apiListing   `json:"folderChildren"`
	PasswordInvalid bool          `json:"passwordInvalid"`
	Message         string        `json:"message"`
	Errors          []any         `json:"errors"`
}

func parseAPITime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	layouts := []string{time.RFC3339Nano, "2006-01-02T15:04:05.000000Z", "2006-01-02 15:04:05"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func (e apiEntry) entry(parentID string) drive.Entry {
	parent := parentID
	if e.ParentID != nil {
		parent = strconv.FormatInt(*e.ParentID, 10)
	}
	size := e.FileSize
	isDir := e.Type == itemTypeFolder
	if isDir {
		size = dirSizeZero
	}
	mod := parseAPITime(e.UpdatedAt)
	if e.ClientLastModified != nil && *e.ClientLastModified > 0 {
		mod = time.UnixMilli(*e.ClientLastModified).UTC()
	}
	rawExtra := map[string]any{
		"hash":         e.Hash,
		"url":          e.URL,
		"workspace_id": e.WorkspaceID,
		"permissions":  e.Permissions,
	}
	if e.FileHash != nil && *e.FileHash != "" {
		rawExtra["file_hash"] = *e.FileHash
	}
	plain := decodeRemoteName(e.Name)
	return drive.WrapRemoteName(drive.Entry{
		ID:        strconv.FormatInt(e.ID, 10),
		ParentID:  parent,
		Name:      plain,
		IsDir:     isDir,
		Size:      size,
		ModTime:   mod,
		CreatedAt: parseAPITime(e.CreatedAt),
		UpdatedAt: parseAPITime(e.UpdatedAt),
		Extra:     rawExtra,
	}, e.Name)
}

func (e apiEntry) isDir() bool { return e.Type == itemTypeFolder }

func entryFileHash(entry drive.Entry) string {
	raw, ok := drive.RawEntryExtra(entry.Extra).(map[string]any)
	if !ok {
		return ""
	}
	value, _ := raw["file_hash"].(string)
	return value
}

func entryHash(entry drive.Entry) string {
	raw, ok := drive.RawEntryExtra(entry.Extra).(map[string]any)
	if !ok {
		return ""
	}
	value, _ := raw["hash"].(string)
	return value
}

func entryURL(entry drive.Entry) string {
	raw, ok := drive.RawEntryExtra(entry.Extra).(map[string]any)
	if !ok {
		return ""
	}
	value, _ := raw["url"].(string)
	return value
}

func mustJSONRaw(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}
