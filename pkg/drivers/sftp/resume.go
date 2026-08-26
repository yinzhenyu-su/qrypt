package sftp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

const sftpUploadPartSize = 8 << 20

func (d *Driver) putSourceResumable(ctx context.Context, req drive.UploadRequest) (entry drive.Entry, err error) {
	started := time.Now()
	defer func() { d.recordOperation(ctx, "upload", path.Join(req.ParentID, req.Name), started, entry.Size, err) }()
	if req.Source == nil {
		return drive.Entry{}, drive.NonRetryable(fmt.Errorf("sftp: upload source is required"))
	}
	parent, err := d.resolveID(req.ParentID)
	if err != nil {
		return drive.Entry{}, err
	}
	client, err := d.getClient(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	size := req.Source.Size()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	sha256Hex, err := session.ContentSHA256Hex(ctx, req.Source, size)
	if err != nil {
		return drive.Entry{}, err
	}
	sessionKey := session.Identity{ParentID: parent, Name: req.Name, Size: size, Fingerprint: sha256Hex}.Key()
	staging := stagingPath(parent, sessionKey)
	if err := d.ensureSessionBinding(sessionKey, parent, req.Name, staging); err != nil {
		return drive.Entry{}, err
	}

	// 进度真相在服务端：staging 文件的连续大小即已传字节数，与本地绑定无关。
	resumeFrom, err := stagingResumeOffset(client, staging, size)
	if err != nil {
		return drive.Entry{}, err
	}
	// 中断可能留下半个分片：回退到分片边界重传整个分片。
	resumeFrom = (resumeFrom / sftpUploadPartSize) * sftpUploadPartSize
	if size == 0 {
		if err := createRemoteFile(client, staging); err != nil {
			return drive.Entry{}, err
		}
	}

	source, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("sftp: upload source open: %w", err)
	}
	defer source.Close()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
	drive.ReportUploadProgress(req.Progress, resumeFrom)
	partCount := uploadPartCount(size, sftpUploadPartSize)
	for part := int(resumeFrom / sftpUploadPartSize); part < partCount; part++ {
		start := int64(part) * sftpUploadPartSize
		length := int64(sftpUploadPartSize)
		if remaining := size - start; remaining < length {
			length = remaining
		}
		partStarted := time.Now()
		partErr := d.uploadSFTPPart(ctx, client, staging, source, start, length)
		partBytes := length
		if partErr != nil {
			partBytes = 0
		}
		d.recordOperation(ctx, "upload_part", staging, partStarted, partBytes, partErr)
		if partErr != nil {
			return drive.Entry{}, fmt.Errorf("sftp: upload part %d: %w", part, partErr)
		}
		if d.sessions != nil {
			d.sessions.Touch(sessionKey)
		}
	}

	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	if !req.ModTime.IsZero() {
		if err := client.Chtimes(staging, req.ModTime, req.ModTime); err != nil {
			return drive.Entry{}, fmt.Errorf("sftp: set mtime %q: %w", staging, err)
		}
	}
	commitStarted := time.Now()
	finalPath := path.Join(parent, req.Name)
	if err := client.Rename(staging, finalPath); err != nil {
		d.recordOperation(ctx, "upload_commit", finalPath, commitStarted, 0, err)
		return drive.Entry{}, fmt.Errorf("sftp: commit upload %q: %w", req.Name, classifyError(err))
	}
	d.recordOperation(ctx, "upload_commit", finalPath, commitStarted, size, nil)
	info, err := client.Stat(finalPath)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("sftp: stat upload %q: %w", req.Name, classifyError(err))
	}
	// provider 侧 commit 已成功：绑定清理尽力而为，不阻塞成功返回；
	// 残留绑定由过期回收兜底。
	if d.sessions != nil {
		d.sessions.Delete(sessionKey)
	}
	entry = drive.Entry{ID: finalPath, ParentID: parent, Name: req.Name, Size: info.Size(), ModTime: info.ModTime(), UpdatedAt: info.ModTime()}
	return entry, nil
}

// ensureSessionBinding 预留绑定：上传开始前必须落盘，崩溃后回收器才知道
// 该 staging 文件属于哪个会话。写入失败则拒绝开始上传，避免产生无绑定
// 可循的孤儿 staging。
func (d *Driver) ensureSessionBinding(sessionKey, parent, name, staging string) error {
	if d.sessions == nil {
		return nil
	}
	if _, ok := d.sessions.Get(sessionKey); ok {
		return nil
	}
	token, err := json.Marshal(sftpToken{ParentID: parent, Name: name, StagingPath: staging, PartSize: sftpUploadPartSize})
	if err != nil {
		return fmt.Errorf("sftp: encode upload session: %w", err)
	}
	if err := d.sessions.Create(sessionKey, token); err != nil {
		return fmt.Errorf("sftp: persist upload session: %w", err)
	}
	return nil
}

// stagingResumeOffset 返回 staging 文件已有的连续字节数；文件不存在返回 0，
// 文件比目标还大视为损坏清掉重传。
func stagingResumeOffset(client *sftp.Client, stagingPath string, size int64) (int64, error) {
	info, err := client.Stat(stagingPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("sftp: stat staging file %q: %w", stagingPath, classifyError(err))
	}
	if info.Size() > size {
		if err := client.Remove(stagingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("sftp: remove corrupt staging file %q: %w", stagingPath, err)
		}
		return 0, nil
	}
	return info.Size(), nil
}

func uploadPartCount(size, partSize int64) int {
	if size <= 0 {
		return 0
	}
	return int((size + partSize - 1) / partSize)
}

func (d *Driver) uploadSFTPPart(ctx context.Context, client *sftp.Client, remotePath string, source drive.ReadOnlyFile, offset, size int64) error {
	partCtx, cancel := context.WithTimeout(ctx, sftpUploadPartTimeout)
	defer cancel()
	unblock := make(chan struct{})
	defer close(unblock)
	go func() {
		select {
		case <-partCtx.Done():
			d.closeIfUnresponsive(client)
		case <-unblock:
		}
	}()
	if _, err := source.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek source to %d: %w", offset, err)
	}
	file, err := client.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return fmt.Errorf("open remote staging file: %w", err)
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		return fmt.Errorf("seek remote staging file to %d: %w", offset, err)
	}
	body := d.limiter.LimitUpload(partCtx, io.LimitReader(source, size))
	written, err := io.CopyN(file, contextReader{ctx: partCtx, reader: body}, size)
	if err == nil {
		err = partCtx.Err()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("write remote part at %d: %w", offset, err)
	}
	if closeErr != nil {
		return fmt.Errorf("close remote part at %d: %w", offset, closeErr)
	}
	if written != size {
		return fmt.Errorf("wrote %d bytes at %d, want %d", written, offset, size)
	}
	return nil
}

func createRemoteFile(client *sftp.Client, remotePath string) error {
	file, err := client.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return fmt.Errorf("create remote staging file %q: %w", remotePath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close remote staging file %q: %w", remotePath, err)
	}
	return nil
}
