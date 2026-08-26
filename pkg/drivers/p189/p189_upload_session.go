package p189

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// p189Token 是持久化的 provider 上传句柄 + 压缩的本地确认记录。p189 无
// 分片进度查询接口，按设计文档的回退原则（服务端不可查询时以本地确认记录
// 为依据，并允许幂等重传）保存已确认分片位图（session.ConfirmedBitmap）：
// 每片确认后经 TouchWith 节流落盘（≤1 次/分钟），崩溃最多丢一分钟确认，
// 对应分片重传幂等覆盖，安全。
type p189Token struct {
	UploadFileID string `json:"upload_file_id"`
	PartSize     int64  `json:"part_size"`
	Confirmed    []byte `json:"confirmed,omitempty"` // 位图：bit(n-1) = 分片 n 已确认
}

func (d *Driver) installSessionIndex(store drive.StateStore) {
	d.sessionStoreMu.Lock()
	defer d.sessionStoreMu.Unlock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessions = session.NewIndex(store, p189SessionFile, session.IndexOptions{
		OnError: func(err error) {
			logging.L.Warnf("189: upload session state failed: %v", err)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.sessionCancel = cancel
	d.expireSessions()
	go session.RunExpirer(ctx, p189SessionExpiryEvery, d.expireSessions)
}

func (d *Driver) expireSessions() {
	if d.sessions != nil {
		d.sessions.Expire(p189SessionMaxAge, time.Now(), d.reclaimUploadSession)
	}
}

// reclaimUploadSession 释放一个过期上传。189 无 abort 上传接口，provider
// 侧会话到期自动失效；回收只需要丢弃本地绑定（幂等，无副作用）。
func (d *Driver) reclaimUploadSession(s session.Session) error {
	var tok p189Token
	if err := json.Unmarshal(s.Token, &tok); err != nil {
		return nil
	}
	if tok.UploadFileID == "" {
		return nil
	}
	// 无 abort 端点：provider 会话到期后自动释放，绑定直接丢弃。
	return nil
}
