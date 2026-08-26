package yun139

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

// yun139Token 是持久化的 provider 上传句柄 + 本地确认位图。139 无分片进度
// 查询接口，按设计文档的回退原则（服务端不可查询时以本地确认记录为依据，
// 并允许幂等重传）经 TouchWith 节流落盘（≤1 次/分钟）；恢复时上传 URL 用
// /file/getUploadUrl 全部现取，避免复用会过期的预签名 URL。
type yun139Token struct {
	FileID    string `json:"file_id"`
	FileName  string `json:"file_name,omitempty"`
	UploadID  string `json:"upload_id"`
	PartSize  int64  `json:"part_size"`
	Confirmed []byte `json:"confirmed,omitempty"` // 位图：bit(n-1) = 分片 n 已确认
}

func (t yun139Token) createResp() personalUploadResp {
	var resp personalUploadResp
	resp.Success = true
	resp.Data.FileID = t.FileID
	resp.Data.FileName = t.FileName
	resp.Data.UploadID = t.UploadID
	return resp
}

func (d *Driver) installSessionIndex(store drive.StateStore) {
	d.sessionStoreMu.Lock()
	defer d.sessionStoreMu.Unlock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessions = session.NewIndex(store, yun139SessionFile, session.IndexOptions{
		OnError: func(err error) {
			d.setLastError(fmt.Errorf("139: upload session state: %w", err))
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.sessionCancel = cancel
	d.expireSessions()
	go session.RunExpirer(ctx, yun139SessionExpiryEvery, d.expireSessions)
}

func (d *Driver) expireSessions() {
	if d.sessions != nil {
		d.sessions.Expire(yun139SessionMaxAge, time.Now(), d.reclaimUploadSession)
	}
}

// reclaimUploadSession 释放一个过期上传。139 无公开 abort 上传接口，provider
// 侧会话到期自动失效；回收只需要丢弃本地绑定（幂等，无副作用）。
func (d *Driver) reclaimUploadSession(s session.Session) error {
	var tok yun139Token
	if err := json.Unmarshal(s.Token, &tok); err != nil {
		return nil
	}
	if tok.FileID == "" || tok.UploadID == "" {
		return nil
	}
	// 无 abort 端点：provider 会话到期后自动释放，绑定直接丢弃。
	return nil
}
