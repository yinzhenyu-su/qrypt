package p189

import (
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/util/uploadsession"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) loadUploadSession(key string) (p189UploadSession, bool) {
	session, ok := d.uploadSessionStore().Load(key)
	if session.CompletedParts == nil {
		session.CompletedParts = map[int]bool{}
	}
	return session, ok
}

func (d *Driver) saveUploadSession(session p189UploadSession) {
	d.uploadSessionStore().Save(session)
}

func (d *Driver) deleteUploadSession(key string) {
	d.uploadSessionStore().Delete(key)
}

func (d *Driver) uploadSessionStore() *uploadsession.UploadSessionStore[p189UploadSession] {
	return uploadsession.NewUploadSessionStore(uploadsession.UploadSessionStoreOptions[p189UploadSession]{
		Store:      d.stateStore,
		File:       p189UploadSessionStateFile,
		MaxAge:     p189UploadSessionMaxAge,
		MaxEntries: p189UploadSessionMaxEntries,
		Key: func(session p189UploadSession) string {
			return session.Key
		},
		Valid: func(key string, session p189UploadSession) bool {
			return session.Key != "" && session.UploadFileID != "" && len(session.CompletedParts) > 0
		},
		UpdatedAt: func(session p189UploadSession) time.Time {
			return session.SavedAt
		},
		Touch: func(session *p189UploadSession, now time.Time) {
			session.SavedAt = now
		},
		OnError: func(err error) {
			logging.L.Warnf("189: upload session state failed: %v", err)
		},
	})
}

func (d *Driver) resumedUploadSessionError(resumed bool, key string, err error) error {
	if resumed && (drive.IsNonRetryable(err) || invalidResumedUploadSession(err)) {
		d.deleteUploadSession(key)
		return fmt.Errorf("189: resumed upload session invalid, will retry from scratch: %v", err)
	}
	return err
}
