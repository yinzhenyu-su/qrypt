package quark

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) uploadSessionKey(parentID, name string, size int64, hashData map[string]any) string {
	md5Hex, _ := hashData["md5"].(string)
	sha1Hex, _ := hashData["sha1"].(string)
	return util.UploadSessionKey(parentID, name, size, md5Hex, sha1Hex)
}

func (d *Driver) loadUploadSession(key string) (quarkUploadSession, bool) {
	return d.uploadSessionStore().Load(key)
}

func (d *Driver) saveUploadSession(session quarkUploadSession) {
	d.uploadSessionStore().Save(session)
}

func (d *Driver) deleteUploadSession(key string) {
	d.uploadSessionStore().Delete(key)
}

func (d *Driver) pruneStoredUploadSessions() {
	d.uploadSessionStore().Prune()
}

func (d *Driver) prunedUploadSessions(state uploadSessionState, now time.Time) (uploadSessionState, bool) {
	state.Version = 1
	sessions, changed := d.uploadSessionStore().PrunedForTest(state.Sessions, now)
	state.Sessions = sessions
	return state, changed
}

func (d *Driver) uploadSessionStore() *util.UploadSessionStore[quarkUploadSession] {
	return util.NewUploadSessionStore(util.UploadSessionStoreOptions[quarkUploadSession]{
		Store:      d.stateStore,
		File:       quarkUploadSessionStateFile,
		MaxAge:     quarkUploadSessionMaxAge,
		MaxEntries: quarkUploadSessionMaxEntries,
		Key: func(session quarkUploadSession) string {
			return session.Key
		},
		Valid: func(key string, session quarkUploadSession) bool {
			return session.Key != "" && len(session.Etags) > 0
		},
		UpdatedAt: func(session quarkUploadSession) time.Time {
			return session.UpdatedAt
		},
		Touch: func(session *quarkUploadSession, now time.Time) {
			session.UpdatedAt = now
		},
		OnError: func(err error) {
			logging.L.Warnf("[QUARK] upload session state failed err=%v", err)
		},
	})
}

func (d *Driver) resumedUploadSessionError(resumed bool, key string, err error) error {
	if resumed && (drive.IsNonRetryable(err) || invalidResumedUploadSession(err)) {
		d.deleteUploadSession(key)
		return fmt.Errorf("quark: resumed upload session invalid, will retry from scratch: %v", err)
	}
	return err
}

func uploadSessionFromPre(key, parentID, name string, size int64, hashData map[string]any, pre upPreResp, partSize int) quarkUploadSession {
	md5Hex, _ := hashData["md5"].(string)
	sha1Hex, _ := hashData["sha1"].(string)
	return quarkUploadSession{
		Key:       key,
		ParentID:  parentID,
		Name:      name,
		Size:      size,
		MD5:       md5Hex,
		SHA1:      sha1Hex,
		TaskID:    pre.Data.TaskID,
		UploadID:  pre.Data.UploadID,
		ObjKey:    pre.Data.ObjKey,
		UploadURL: pre.Data.UploadURL,
		Fid:       pre.Data.Fid,
		Bucket:    pre.Data.Bucket,
		Callback:  append(json.RawMessage(nil), pre.Data.Callback...),
		AuthInfo:  pre.Data.AuthInfo,
		PartSize:  partSize,
		Etags:     map[int]string{},
	}
}

func (s quarkUploadSession) preResp() upPreResp {
	var pre upPreResp
	pre.Data.TaskID = s.TaskID
	pre.Data.UploadID = s.UploadID
	pre.Data.ObjKey = s.ObjKey
	pre.Data.UploadURL = s.UploadURL
	pre.Data.Fid = s.Fid
	pre.Data.Bucket = s.Bucket
	pre.Data.Callback = append(json.RawMessage(nil), s.Callback...)
	pre.Data.AuthInfo = s.AuthInfo
	pre.Metadata.PartSize = s.PartSize
	return pre
}

func (d *Driver) setUploadDebug(taskID string, item quarkUploadDebug) {
	if taskID == "" {
		return
	}
	d.debugMu.Lock()
	d.debugUploads[taskID] = item
	d.debugMu.Unlock()
}

func (d *Driver) updateUploadDebug(taskID string, update func(*quarkUploadDebug)) {
	if taskID == "" {
		return
	}
	d.debugMu.Lock()
	item := d.debugUploads[taskID]
	update(&item)
	item.UpdatedAt = time.Now()
	d.debugUploads[taskID] = item
	d.debugMu.Unlock()
}

func (d *Driver) setUploadDebugError(taskID string, err error) {
	if err == nil {
		return
	}
	d.updateUploadDebug(taskID, func(item *quarkUploadDebug) {
		item.Stage = "error"
		item.LastError = err.Error()
	})
}

func (d *Driver) finishUploadDebug(taskID string) {
	if taskID == "" {
		return
	}
	d.debugMu.Lock()
	delete(d.debugUploads, taskID)
	d.debugMu.Unlock()
}

func (d *Driver) activeUploadDebug() []quarkUploadDebug {
	d.debugMu.Lock()
	defer d.debugMu.Unlock()
	uploads := make([]quarkUploadDebug, 0, len(d.debugUploads))
	for _, upload := range d.debugUploads {
		uploads = append(uploads, upload)
	}
	return uploads
}

type uploadSessionState struct {
	Version  int                           `json:"version"`
	Sessions map[string]quarkUploadSession `json:"sessions,omitempty"`
}

type quarkUploadSession struct {
	Key       string          `json:"key"`
	ParentID  string          `json:"parent_id"`
	Name      string          `json:"name"`
	Size      int64           `json:"size"`
	MD5       string          `json:"md5"`
	SHA1      string          `json:"sha1"`
	TaskID    string          `json:"task_id"`
	UploadID  string          `json:"upload_id"`
	ObjKey    string          `json:"obj_key"`
	UploadURL string          `json:"upload_url"`
	Fid       string          `json:"fid"`
	Bucket    string          `json:"bucket"`
	Callback  json.RawMessage `json:"callback,omitempty"`
	AuthInfo  string          `json:"auth_info"`
	PartSize  int             `json:"part_size"`
	Etags     map[int]string  `json:"etags,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type quarkUploadDebug struct {
	Name           string    `json:"name"`
	ParentID       string    `json:"parent_id"`
	TaskID         string    `json:"task_id"`
	UploadID       string    `json:"upload_id"`
	ObjKey         string    `json:"obj_key,omitempty"`
	PartSize       int64     `json:"part_size"`
	PartsSubmitted int       `json:"parts_submitted"`
	PartsCompleted int       `json:"parts_completed"`
	BytesTotal     int64     `json:"bytes_total"`
	BytesRead      int64     `json:"bytes_read"`
	Stage          string    `json:"stage"`
	LastError      string    `json:"last_error,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
