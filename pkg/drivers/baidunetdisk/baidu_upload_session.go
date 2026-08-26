package baidunetdisk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

// baiduToken 是持久化的 provider 上传句柄；分片进度不落盘，恢复时用
// superfile2 list 从服务端重建已上传 partseq。
type baiduToken struct {
	UploadID string `json:"upload_id"`
	PartSize int64  `json:"part_size"`
}

func (d *Driver) installSessionIndex(store drive.StateStore) {
	d.sessionStoreMu.Lock()
	defer d.sessionStoreMu.Unlock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessions = session.NewIndex(store, baiduSessionFile, session.IndexOptions{
		OnError: func(err error) {
			d.setLastError(fmt.Errorf("baidu_netdisk: upload session state: %w", err))
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.sessionCancel = cancel
	d.expireSessions()
	go session.RunExpirer(ctx, baiduSessionExpiryEvery, d.expireSessions)
}

func (d *Driver) expireSessions() {
	if d.sessions != nil {
		d.sessions.Expire(baiduSessionMaxAge, time.Now(), d.reclaimUploadSession)
	}
}

// reclaimUploadSession 释放一个过期上传。百度盘没有公开的 abort 上传接口，
// provider 侧 uploadid 到期自动失效；回收只需要丢弃本地绑定（幂等，无副作用）。
func (d *Driver) reclaimUploadSession(s session.Session) error {
	var tok baiduToken
	if err := json.Unmarshal(s.Token, &tok); err != nil {
		return nil
	}
	if tok.UploadID == "" {
		return nil
	}
	// 无 abort 端点：provider 会话到期后自动释放，绑定直接丢弃。
	return nil
}

// listUploadedPartSeqs 用 superfile2 list 从服务端重建已上传 partseq
// （本地零分片状态）。查询失败返回错误，由调用方决定按临时故障全量重传。
func (d *Driver) listUploadedPartSeqs(ctx context.Context, remotePath, uploadID string) ([]int, error) {
	var resp struct {
		BlockList []int `json:"block_list"`
		Info      []struct {
			BlockList []int `json:"block_list"`
		} `json:"info"`
		UploadedParts []int `json:"uploaded_parts"`
	}
	if err := d.doRequest(ctx, http.MethodGet, d.uploadAPI+"/rest/2.0/pcs/superfile2",
		map[string]string{"method": "list", "type": "tmpfile", "path": remotePath, "uploadid": uploadID, "partseq": "0", "chunk": "1"},
		nil, &resp); err != nil {
		return nil, err
	}
	seen := make(map[int]bool, len(resp.BlockList)+len(resp.Info)+len(resp.UploadedParts))
	var seqs []int
	add := func(seq int) {
		if seq < 0 || seen[seq] {
			return
		}
		seen[seq] = true
		seqs = append(seqs, seq)
	}
	for _, seq := range resp.BlockList {
		add(seq)
	}
	for _, info := range resp.Info {
		for _, seq := range info.BlockList {
			add(seq)
		}
	}
	for _, seq := range resp.UploadedParts {
		add(seq)
	}
	return seqs, nil
}

// partSeqs 生成 0 起始的分片序号列表（百度盘 partseq 从 0 开始）。
func partSeqs(size, partSize int64) []int {
	if size <= 0 || partSize <= 0 {
		return nil
	}
	count := int((size + partSize - 1) / partSize)
	seqs := make([]int, count)
	for i := range seqs {
		seqs[i] = i
	}
	return seqs
}
