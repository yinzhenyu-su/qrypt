package quark

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// quarkToken 是持久化的 provider 上传句柄 + 每分片确认 ETag（OSS Complete
// 需要全部 ETag）。quark 无分片进度查询接口，按设计文档的回退原则（服务端
// 不可查询时以本地确认记录为依据，并允许幂等重传）经 TouchWith 节流落盘
// （≤1 次/分钟）：崩溃最多丢一分钟确认，对应分片重传幂等覆盖，安全。
type quarkToken struct {
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
}

func (t quarkToken) preResp() upPreResp {
	var pre upPreResp
	pre.Data.TaskID = t.TaskID
	pre.Data.UploadID = t.UploadID
	pre.Data.ObjKey = t.ObjKey
	pre.Data.UploadURL = t.UploadURL
	pre.Data.Fid = t.Fid
	pre.Data.Bucket = t.Bucket
	pre.Data.Callback = append(json.RawMessage(nil), t.Callback...)
	pre.Data.AuthInfo = t.AuthInfo
	pre.Metadata.PartSize = t.PartSize
	return pre
}

func (d *Driver) installSessionIndex(store drive.StateStore) {
	d.sessionStoreMu.Lock()
	defer d.sessionStoreMu.Unlock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessions = session.NewIndex(store, quarkSessionFile, session.IndexOptions{
		OnError: func(err error) {
			logging.L.Warnf("[QUARK] upload session state failed err=%v", err)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.sessionCancel = cancel
	d.expireSessions()
	go session.RunExpirer(ctx, quarkSessionExpiryEvery, d.expireSessions)
}

func (d *Driver) expireSessions() {
	if d.sessions != nil {
		d.sessions.Expire(quarkSessionMaxAge, time.Now(), d.reclaimUploadSession)
	}
}

// reclaimUploadSession 释放一个过期上传。quark 无 abort 上传接口，provider
// 侧会话到期自动失效；回收只需要丢弃本地绑定（幂等，无副作用）。
func (d *Driver) reclaimUploadSession(s session.Session) error {
	var tok quarkToken
	if err := json.Unmarshal(s.Token, &tok); err != nil {
		return nil
	}
	if tok.UploadID == "" {
		return nil
	}
	// 无 abort 端点：provider 会话到期后自动释放，绑定直接丢弃。
	return nil
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
