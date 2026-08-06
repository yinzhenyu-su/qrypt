package aliyundrive

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/yinzhenyu/qrypt/internal/util/uploadsession"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) uploadSessionKey(parentID, name string, size int64, sha1Hex string) string {
	return uploadsession.Key(parentID, name, size, sha1Hex)
}

func (d *Driver) loadUploadSession(key string) (aliyunUploadSession, bool) {
	session, ok := d.uploadSessionStore().Load(key)
	if session.CompletedParts == nil {
		session.CompletedParts = map[int]bool{}
	}
	return session, ok
}

func (d *Driver) saveUploadSession(session aliyunUploadSession) {
	d.uploadSessionStore().Save(session)
}

func (d *Driver) deleteUploadSession(key string) {
	d.uploadSessionStore().Delete(key)
}

func (d *Driver) pruneStoredUploadSessions() {
	d.uploadSessionStore().Prune()
}

func (d *Driver) uploadSessionStore() *uploadsession.Store[aliyunUploadSession] {
	return uploadsession.NewStore(uploadsession.StoreOptions[aliyunUploadSession]{
		Store:      d.stateStore,
		File:       aliyunUploadSessionStateFile,
		MaxAge:     aliyunUploadSessionMaxAge,
		MaxEntries: aliyunUploadSessionMaxEntries,
		Key: func(session aliyunUploadSession) string {
			return session.Key
		},
		Valid: func(key string, session aliyunUploadSession) bool {
			return session.Key != "" && session.UploadID != "" && session.FileID != "" && len(session.PartInfoList) > 0 && len(session.CompletedParts) > 0
		},
		UpdatedAt: func(session aliyunUploadSession) time.Time {
			return session.SavedAt
		},
		Touch: func(session *aliyunUploadSession, now time.Time) {
			session.SavedAt = now
		},
		OnError: func(err error) {
			d.setLastError(fmt.Errorf("aliyundrive: upload session state: %w", err))
		},
	})
}

func (s aliyunUploadSession) createResp() createResp {
	return createResp{
		FileID:       s.FileID,
		Name:         s.Name,
		Size:         s.Size,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
		UploadID:     s.UploadID,
		PartInfoList: append([]uploadPartInfo(nil), s.PartInfoList...),
	}
}

func (d *Driver) resumedUploadSessionError(resumed bool, key string, err error) error {
	if resumed && (drive.IsNonRetryable(err) || invalidResumedUploadSession(err)) {
		d.deleteUploadSession(key)
		return fmt.Errorf("aliyundrive: resumed upload session invalid, will retry from scratch: %v", err)
	}
	return err
}

func uploadSessionFromCreate(key, parentID, name string, size int64, sha1Hex string, partSize int64, create createResp) aliyunUploadSession {
	return aliyunUploadSession{
		Key:            key,
		ParentID:       parentID,
		Name:           name,
		Size:           size,
		SHA1:           sha1Hex,
		FileID:         create.FileID,
		UploadID:       create.UploadID,
		PartSize:       partSize,
		PartInfoList:   append([]uploadPartInfo(nil), create.PartInfoList...),
		CompletedParts: map[int]bool{},
		CreatedAt:      create.CreatedAt,
		UpdatedAt:      create.UpdatedAt,
	}
}

func invalidResumedUploadSession(err error) bool {
	var apiErr *apiStatusError
	if errors.As(err, &apiErr) {
		return apiErr.status == http.StatusConflict || apiErr.status == http.StatusNotFound || apiErr.status == http.StatusGone || apiErr.status == http.StatusBadRequest
	}
	return false
}
