package p115open

import (
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"
)

const p115OpenUploadSessionStateFile = "115_open_upload_sessions.json"
const p115OpenUploadSessionMaxAge = 24 * time.Hour
const p115OpenUploadSessionMaxEntries = 1024

type p115OpenUploadSessionState struct {
	Version  int                              `json:"version"`
	Sessions map[string]p115OpenUploadSession `json:"sessions,omitempty"`
}

type p115OpenUploadSession struct {
	Key       string    `json:"key"`
	ParentID  string    `json:"parent_id"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	SHA1      string    `json:"sha1"`
	Bucket    string    `json:"bucket"`
	Object    string    `json:"object"`
	UploadID  string    `json:"upload_id"`
	PartSize  int64     `json:"part_size"`
	Parts     []ossPart `json:"parts,omitempty"`
	Callback  string    `json:"callback,omitempty"`
	CallbackV string    `json:"callback_var,omitempty"`
	SavedAt   time.Time `json:"saved_at"`
}

type ossPart struct {
	Number int    `json:"number"`
	ETag   string `json:"etag"`
}

func (s p115OpenUploadSession) partsByNumber() map[int]oss.UploadPart {
	parts := make(map[int]oss.UploadPart, len(s.Parts))
	for _, part := range s.Parts {
		if part.Number <= 0 || part.ETag == "" {
			continue
		}
		parts[part.Number] = oss.UploadPart{PartNumber: part.Number, ETag: part.ETag}
	}
	return parts
}

func (d *Driver) loadUploadSession(key string) (p115OpenUploadSession, bool) {
	return d.uploadSessionStore().Load(key)
}

func (d *Driver) saveUploadSession(session p115OpenUploadSession) {
	d.uploadSessionStore().Save(session)
}

func (d *Driver) deleteUploadSession(key string) {
	d.uploadSessionStore().Delete(key)
}

func (d *Driver) uploadSessionStore() *util.UploadSessionStore[p115OpenUploadSession] {
	return util.NewUploadSessionStore(util.UploadSessionStoreOptions[p115OpenUploadSession]{
		Store:      d.stateStore,
		File:       p115OpenUploadSessionStateFile,
		MaxAge:     p115OpenUploadSessionMaxAge,
		MaxEntries: p115OpenUploadSessionMaxEntries,
		Key: func(session p115OpenUploadSession) string {
			return session.Key
		},
		Valid: func(key string, session p115OpenUploadSession) bool {
			return session.Key != "" && session.Bucket != "" && session.Object != "" && session.UploadID != "" && session.PartSize > 0 && len(session.Parts) > 0
		},
		UpdatedAt: func(session p115OpenUploadSession) time.Time {
			return session.SavedAt
		},
		Touch: func(session *p115OpenUploadSession, now time.Time) {
			session.SavedAt = now
		},
		OnError: func(err error) {
			logging.L.Warnf("115_open: upload session state failed: %v", err)
		},
	})
}
