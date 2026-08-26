package sftp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

const (
	sftpSessionFile        = "sftp_upload_sessions.json"
	sftpSessionMaxAge      = session.DefaultMaxAge
	sftpSessionExpiryEvery = 4 * time.Hour
)

// sftpToken 是绑定中保存的 provider 侧上传引用。staging 路径是确定性的
// （由内容键推导），存下来是为了精确回收孤儿 staging 文件，不需要远端遍历。
type sftpToken struct {
	ParentID    string `json:"parent_id"`
	Name        string `json:"name"`
	StagingPath string `json:"staging_path"`
	PartSize    int64  `json:"part_size,omitempty"`
}

// installSessionIndex 接入绑定索引并启动过期回收：进度以远端 staging 文件的
// 连续大小为准（stat），本地只保存"内容键 → staging 路径"的绑定用于回收。
func (d *Driver) installSessionIndex(store drive.StateStore) {
	d.sessionStoreMu.Lock()
	defer d.sessionStoreMu.Unlock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessions = session.NewIndex(store, sftpSessionFile, session.IndexOptions{
		OnError: func(err error) {
			logging.L.Warnf("[SFTP] upload session state failed err=%v", err)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.sessionCancel = cancel
	d.expireSessions()
	go session.RunExpirer(ctx, sftpSessionExpiryEvery, d.expireSessions)
}

// stagingPath 返回一次上传的确定性远端暂存路径：同一内容键永远同一路径，
// 使恢复不依赖任何本地记录。
func stagingPath(parent, sessionKey string) string {
	return path.Join(parent, ".qrypt-sftp-upload-"+sessionKey)
}

// expireSessions 回收超过 maxAge 未活动的绑定；每个过期绑定先清理远端
// staging 文件（幂等），成功后才删除绑定。删除绑定失败或远端清理失败都会
// 保留绑定，等待下一轮。
func (d *Driver) expireSessions() {
	if d.sessions == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d.sessions.Expire(sftpSessionMaxAge, time.Now(), func(binding session.Session) error {
		return d.reclaimUploadSession(ctx, binding)
	})
}

// reclaimUploadSession 幂等回收一个过期绑定对应的远端 staging 文件。
func (d *Driver) reclaimUploadSession(ctx context.Context, binding session.Session) error {
	var tok sftpToken
	if err := json.Unmarshal(binding.Token, &tok); err != nil {
		return nil
	}
	if tok.StagingPath == "" {
		return nil
	}
	client, err := d.getClient(ctx)
	if err != nil {
		return err
	}
	if err := client.Remove(tok.StagingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sftp: reclaim staging file %q: %w", tok.StagingPath, classifyError(err))
	}
	return nil
}
