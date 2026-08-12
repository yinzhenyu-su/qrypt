// Package p115 implements the 115 cloud drive driver.
package p115

import (
	"fmt"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/uploadsession"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

func (s p115UploadSession) uploadParams() *driver115.UploadOSSParams {
	params := &driver115.UploadOSSParams{
		SHA1:   s.SHA1,
		Bucket: s.Bucket,
		Object: s.Object,
	}
	params.Callback.Callback = s.Callback
	params.Callback.CallbackVar = s.CallbackV
	return params
}

func (s p115UploadSession) partsByNumber() map[int]oss.UploadPart {
	parts := make(map[int]oss.UploadPart, len(s.Parts))
	for _, part := range s.Parts {
		if part.Number <= 0 || part.ETag == "" {
			continue
		}
		parts[part.Number] = oss.UploadPart{PartNumber: part.Number, ETag: part.ETag}
	}
	return parts
}

func (d *Driver) loadUploadSession(key string) (p115UploadSession, bool) {
	return d.uploadSessionStore().Load(key)
}

func (d *Driver) saveUploadSession(session p115UploadSession) {
	d.uploadSessionStore().Save(session)
}

func (d *Driver) deleteUploadSession(key string) {
	d.uploadSessionStore().Delete(key)
}

func (d *Driver) uploadSessionStore() *uploadsession.Store[p115UploadSession] {
	return uploadsession.NewStore(uploadsession.StoreOptions[p115UploadSession]{
		Store:      d.stateStore,
		File:       p115UploadSessionStateFile,
		MaxAge:     p115UploadSessionMaxAge,
		MaxEntries: p115UploadSessionMaxEntries,
		Key: func(session p115UploadSession) string {
			return session.Key
		},
		Valid: func(key string, session p115UploadSession) bool {
			return session.Key != "" && session.Bucket != "" && session.Object != "" && session.UploadID != "" && session.PartSize > 0 && len(session.Parts) > 0
		},
		UpdatedAt: func(session p115UploadSession) time.Time {
			return session.SavedAt
		},
		Touch: func(session *p115UploadSession, now time.Time) {
			session.SavedAt = now
		},
		OnError: func(err error) {
			logging.L.Warnf("115: upload session state failed: %v", err)
		},
	})
}

func (d *Driver) resumedUploadSessionError(resumed bool, key string, err error) error {
	if resumed && invalidResumedUploadSession(err) {
		d.deleteUploadSession(key)
		return fmt.Errorf("115: resumed upload session invalid, will retry from scratch: %v", err)
	}
	return err
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err != nil {
		return n, err
	}
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, nil
}
