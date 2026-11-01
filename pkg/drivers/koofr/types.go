package koofr

import (
	"encoding/json"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// apiErrorEnvelope is the standard Koofr error body:
// {"error":{"code":...,"message":...},"requestId":"uuid"}. Some endpoints
// answer non-JSON bodies, so parsing always tolerates failure.
type apiErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"requestId"`
}

type apiUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Level int    `json:"level"`
}

type apiMount struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsPrimary  bool   `json:"isPrimary"`
	Online     bool   `json:"online"`
	CanWrite   bool   `json:"canWrite"`
	CanUpload  bool   `json:"canUpload"`
	OverQuota  bool   `json:"overQuota"`
	SpaceTotal int64  `json:"spaceTotal"` // MiB
	SpaceUsed  int64  `json:"spaceUsed"`  // MiB
}

type apiMountList struct {
	Mounts []apiMount `json:"mounts"`
}

// UnmarshalJSON accepts both the documented array form and an enveloped
// {"mounts":[...]} object so either provider shape decodes.
func (m *apiMountList) UnmarshalJSON(data []byte) error {
	var array []apiMount
	if err := json.Unmarshal(data, &array); err == nil {
		m.Mounts = array
		return nil
	}
	var enveloped struct {
		Mounts []apiMount `json:"mounts"`
	}
	if err := json.Unmarshal(data, &enveloped); err != nil {
		return err
	}
	m.Mounts = enveloped.Mounts
	return nil
}

// apiFile is one entry in a listing, info response, or upload result.
type apiFile struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`     // "file" | "dir"
	Modified    int64           `json:"modified"` // milliseconds
	Size        int64           `json:"size"`
	ContentType string          `json:"contentType"`
	Hash        string          `json:"hash"` // MD5 hex
	Tags        json.RawMessage `json:"tags"`
}

type apiFileList struct {
	Files []apiFile `json:"files"`
}

// entry maps a provider file onto a path-addressed drive entry. parent is the
// absolute provider path of the containing directory; the entry id is the
// absolute path so reads, writes, and share paths can use it directly.
func (f apiFile) entry(parent string) drive.Entry {
	isDir := f.Type == "dir"
	// External mounts (e.g. Google Drive) report Google Docs/shortcut entries
	// with size=-1; the entry contract expects a byte size, so an unknown or
	// non-binary size maps to 0 (reads then short-circuit to empty instead of
	// requesting bytes the provider cannot serve).
	size := f.Size
	if isDir || size < 0 {
		size = 0
	}
	extra := map[string]any{}
	if f.Hash != "" {
		extra["hash"] = f.Hash
	}
	if f.ContentType != "" {
		extra["content_type"] = f.ContentType
	}
	var mod time.Time
	if f.Modified > 0 {
		mod = time.UnixMilli(f.Modified).UTC()
	}
	return drive.Entry{
		ID:       joinPath(parent, f.Name),
		ParentID: parent,
		Name:     f.Name,
		IsDir:    isDir,
		Size:     size,
		ModTime:  mod,
		Extra:    extra,
	}
}

// entryFileHash returns the provider MD5 stored in an entry's extra map.
func entryFileHash(entry drive.Entry) string {
	raw, ok := drive.RawEntryExtra(entry.Extra).(map[string]any)
	if !ok {
		return ""
	}
	hash, _ := raw["hash"].(string)
	return hash
}

// apiLink is a Koofr share link. The password field appears in owner-facing
// responses (password/set echoes the plaintext) and must never be logged.
type apiLink struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	MountID          string `json:"mountId"`
	Path             string `json:"path"`
	Counter          int    `json:"counter"`
	URL              string `json:"url"`
	ShortURL         string `json:"shortUrl"`
	Hash             string `json:"hash"`
	Host             string `json:"host"`
	HasPassword      bool   `json:"hasPassword"`
	PasswordRequired bool   `json:"passwordRequired"`
	Password         string `json:"password"`
	Downloadable     bool   `json:"downloadable"`
	Created          int64  `json:"created"` // milliseconds
	ValidFrom        int64  `json:"validFrom"`
	ValidTo          int64  `json:"validTo"`
}
