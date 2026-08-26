package aliyundrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

// aliyunToken 是持久化的 provider 上传句柄 + 本地确认位图 + createWithFolders
// 下发的分片上传 URL。实测确认：本驱动使用的网关（api.alipan.com +
// auth.alipan.com token）上 /adrive/v2/file/getUploadUrl 与
// /adrive/v1.0/openFile/* 全部 404 不存在（openFile 家族只在 openapi.alipan.com
// 且需要开放平台 token），因此分片进度不能服务端重建，按设计文档的回退原则
// （服务端不可查询时以本地确认记录为依据，并允许幂等重传）经 TouchWith 节流
// 落盘；恢复时复用 create 下发的预签名 URL。
type aliyunToken struct {
	FileID    string           `json:"file_id"`
	UploadID  string           `json:"upload_id"`
	PartSize  int64            `json:"part_size"`
	PartURLs  []uploadPartInfo `json:"part_urls,omitempty"`
	Confirmed []byte           `json:"confirmed,omitempty"` // 位图：bit(n-1) = 分片 n 已确认
}

func (t aliyunToken) createResp(name string, size int64) createResp {
	return createResp{
		FileID:       t.FileID,
		Name:         name,
		Size:         size,
		UploadID:     t.UploadID,
		PartInfoList: append([]uploadPartInfo(nil), t.PartURLs...),
	}
}

func (d *Driver) installSessionIndex(store drive.StateStore) {
	d.sessionStoreMu.Lock()
	defer d.sessionStoreMu.Unlock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessions = session.NewIndex(store, aliyunSessionFile, session.IndexOptions{
		OnError: func(err error) {
			d.setLastError(fmt.Errorf("aliyundrive: upload session state: %w", err))
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.sessionCancel = cancel
	d.expireSessions()
	go session.RunExpirer(ctx, aliyunSessionExpiryEvery, d.expireSessions)
}

func (d *Driver) expireSessions() {
	if d.sessions != nil {
		d.sessions.Expire(aliyunSessionMaxAge, time.Now(), d.reclaimUploadSession)
		d.dropHandlelessBindings()
	}
}

// dropHandlelessBindings 立即清除没有 provider 句柄的绑定（崩溃/被杀进程在
// "预留 → create → 更新句柄" 之间留下的空预留，或句柄解析失败的损坏绑定）。
// 它们不引用任何服务端资源，不需要等 24h 过期；对正在 create 中的并发尝试
// 也无害——句柄更新会用 Create 重建绑定。
func (d *Driver) dropHandlelessBindings() {
	if d.sessions == nil {
		return
	}
	for _, s := range d.sessions.List() {
		var tok aliyunToken
		if err := json.Unmarshal(s.Token, &tok); err != nil || tok.FileID == "" || tok.UploadID == "" {
			d.sessions.Delete(s.Key)
		}
	}
}

// reclaimUploadSession 释放一个过期上传。aliyundrive 没有公开的 abort 上传
// 接口，provider 侧的 file_id/upload_id 由服务端自行过期；回收只需要丢弃
// 本地绑定（幂等，无副作用）。
func (d *Driver) reclaimUploadSession(s session.Session) error {
	var tok aliyunToken
	if err := json.Unmarshal(s.Token, &tok); err != nil {
		return nil
	}
	if tok.FileID == "" || tok.UploadID == "" {
		return nil
	}
	// 无 abort 端点：provider 会话到期后自动释放，绑定直接丢弃。
	return nil
}

func invalidResumedUploadSession(err error) bool {
	var apiErr *apiStatusError
	if errors.As(err, &apiErr) {
		return apiErr.status == http.StatusConflict || apiErr.status == http.StatusNotFound || apiErr.status == http.StatusGone || apiErr.status == http.StatusBadRequest
	}
	return false
}
