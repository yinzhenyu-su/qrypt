package sftp

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/uploadsession"
)

const (
	sftpUploadSessionStateFile  = "sftp_upload_sessions.json"
	sftpUploadSessionMaxAge     = 24 * time.Hour
	sftpUploadSessionMaxEntries = 1024
)

type sftpUploadSession struct {
	Key            string       `json:"key"`
	ParentID       string       `json:"parent_id"`
	Name           string       `json:"name"`
	RemotePath     string       `json:"remote_path"`
	Size           int64        `json:"size"`
	SHA256         string       `json:"sha256"`
	PartSize       int64        `json:"part_size"`
	CompletedParts map[int]bool `json:"completed_parts,omitempty"`
	SavedAt        time.Time    `json:"saved_at"`
}

const sftpUploadPartSize = 8 << 20

func (d *Driver) uploadSessionKey(parentID, name string, size int64, sha256Hex string) string {
	return uploadsession.Key(parentID, name, size, sha256Hex)
}

func (d *Driver) loadUploadSession(key string) (sftpUploadSession, bool) {
	return d.uploadSessionStore().LoadAs(key, func(session sftpUploadSession) sftpUploadSession {
		if session.CompletedParts == nil {
			session.CompletedParts = map[int]bool{}
		}
		return session
	})
}

func (d *Driver) saveUploadSession(session sftpUploadSession) {
	d.uploadSessionStore().Save(session)
}

func (d *Driver) deleteUploadSession(key string) {
	d.uploadSessionStore().Delete(key)
}

func (d *Driver) pruneUploadSessions() {
	d.uploadSessionStore().Prune()
}

func (d *Driver) uploadSessionStore() *uploadsession.Store[sftpUploadSession] {
	d.uploadSessionMu.Lock()
	defer d.uploadSessionMu.Unlock()
	if d.uploadSessions != nil {
		return d.uploadSessions
	}
	d.uploadSessions = newUploadSessionStore(d.stateStore)
	return d.uploadSessions
}

func newUploadSessionStore(stateStore drive.StateStore) *uploadsession.Store[sftpUploadSession] {
	return uploadsession.NewStore(uploadsession.StoreOptions[sftpUploadSession]{
		Store:      stateStore,
		Async:      true,
		File:       sftpUploadSessionStateFile,
		MaxAge:     sftpUploadSessionMaxAge,
		MaxEntries: sftpUploadSessionMaxEntries,
		Key: func(session sftpUploadSession) string {
			return session.Key
		},
		Valid: func(key string, session sftpUploadSession) bool {
			return key != "" && session.Key == key && session.ParentID != "" && session.Name != "" && session.RemotePath != "" && session.Size >= 0 && session.SHA256 != ""
		},
		UpdatedAt: func(session sftpUploadSession) time.Time {
			return session.SavedAt
		},
		Touch: func(session *sftpUploadSession, now time.Time) {
			session.SavedAt = now
		},
		Clone: func(session sftpUploadSession) sftpUploadSession {
			clone := session
			if session.CompletedParts != nil {
				clone.CompletedParts = make(map[int]bool, len(session.CompletedParts))
				for part, completed := range session.CompletedParts {
					clone.CompletedParts[part] = completed
				}
			}
			return clone
		},
	})
}
