package sftp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

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
	sha256Hex, err := sourceSHA256Hex(ctx, req.Source, size)
	if err != nil {
		return drive.Entry{}, err
	}
	sessionKey := d.uploadSessionKey(parent, req.Name, size, sha256Hex)
	session, resumed := d.loadUploadSession(sessionKey)
	if !resumed {
		session = sftpUploadSession{
			Key:            sessionKey,
			ParentID:       parent,
			Name:           req.Name,
			RemotePath:     path.Join(parent, ".qrypt-sftp-upload-"+sessionKey),
			Size:           size,
			SHA256:         sha256Hex,
			PartSize:       sftpUploadPartSize,
			CompletedParts: map[int]bool{},
		}
		if err := removeRemoteFile(client, session.RemotePath); err != nil {
			return drive.Entry{}, err
		}
	} else if session.PartSize <= 0 || session.Size != size || session.SHA256 != sha256Hex {
		resumed = false
		session.CompletedParts = map[int]bool{}
		session.PartSize = sftpUploadPartSize
	}
	if session.CompletedParts == nil {
		session.CompletedParts = map[int]bool{}
	}
	if size == 0 {
		if err := createRemoteFile(client, session.RemotePath); err != nil {
			return drive.Entry{}, err
		}
	}
	remoteSize, err := remoteUploadSize(client, session.RemotePath, resumed)
	if err != nil {
		return drive.Entry{}, err
	}
	if remoteSize > size {
		if err := removeRemoteFile(client, session.RemotePath); err != nil {
			return drive.Entry{}, err
		}
		remoteSize = 0
		session.CompletedParts = map[int]bool{}
	}
	for part := range session.CompletedParts {
		if remoteSize < sftpUploadPartEnd(part, session.PartSize, size) {
			delete(session.CompletedParts, part)
		}
	}
	d.saveUploadSession(session)

	source, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("sftp: upload source open: %w", err)
	}
	defer source.Close()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
	partCount := uploadPartCount(size, session.PartSize)
	for part := 0; part < partCount; part++ {
		start := int64(part) * session.PartSize
		length := session.PartSize
		if remaining := size - start; remaining < length {
			length = remaining
		}
		if session.CompletedParts[part] {
			drive.ReportUploadProgress(req.Progress, length)
			continue
		}
		partStarted := time.Now()
		partErr := uploadSFTPPart(ctx, d.limiter, client, session.RemotePath, source, start, length)
		partBytes := length
		if partErr != nil {
			partBytes = 0
		}
		d.recordOperation(ctx, "upload_part", session.RemotePath, partStarted, partBytes, partErr)
		if partErr != nil {
			return drive.Entry{}, fmt.Errorf("sftp: upload part %d: %w", part, partErr)
		}
		session.CompletedParts[part] = true
		remoteSize = maxInt64(remoteSize, start+length)
		d.saveUploadSession(session)
	}

	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	if !req.ModTime.IsZero() {
		if err := client.Chtimes(session.RemotePath, req.ModTime, req.ModTime); err != nil {
			return drive.Entry{}, fmt.Errorf("sftp: set mtime %q: %w", session.RemotePath, err)
		}
	}
	commitStarted := time.Now()
	if err := client.Rename(session.RemotePath, path.Join(parent, req.Name)); err != nil {
		d.recordOperation(ctx, "upload_commit", path.Join(parent, req.Name), commitStarted, 0, err)
		return drive.Entry{}, fmt.Errorf("sftp: commit upload %q: %w", req.Name, classifyError(err))
	}
	d.recordOperation(ctx, "upload_commit", path.Join(parent, req.Name), commitStarted, size, nil)
	info, err := client.Stat(path.Join(parent, req.Name))
	if err != nil {
		return drive.Entry{}, fmt.Errorf("sftp: stat upload %q: %w", req.Name, classifyError(err))
	}
	d.deleteUploadSession(sessionKey)
	entry = drive.Entry{ID: path.Join(parent, req.Name), ParentID: parent, Name: req.Name, Size: info.Size(), ModTime: info.ModTime(), UpdatedAt: info.ModTime()}
	return entry, nil
}

func sourceSHA256Hex(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (string, error) {
	if sum, ok := drive.SourceHash(source, drive.HashSHA256); ok {
		if len(sum) != sha256.Size {
			return "", drive.NonRetryable(fmt.Errorf("sftp: source SHA-256 metadata has %d bytes, want %d", len(sum), sha256.Size))
		}
		return hex.EncodeToString(sum), nil
	}
	file, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("sftp: hash source open: %w", err)
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(hasher, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", fmt.Errorf("sftp: hash source: %w", copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("sftp: close hash source: %w", closeErr)
	}
	if written != size {
		return "", drive.NonRetryable(fmt.Errorf("sftp: source size mismatch: hashed %d, expected %d", written, size))
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func uploadPartCount(size, partSize int64) int {
	if size <= 0 {
		return 0
	}
	return int((size + partSize - 1) / partSize)
}

func sftpUploadPartEnd(part int, partSize, size int64) int64 {
	end := (int64(part) + 1) * partSize
	if end > size {
		return size
	}
	return end
}

func uploadSFTPPart(ctx context.Context, limiter *drive.BandwidthLimiter, client *sftp.Client, remotePath string, source drive.ReadOnlyFile, offset, size int64) error {
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
	body := limiter.LimitUpload(ctx, io.LimitReader(source, size))
	written, err := io.CopyN(file, contextReader{ctx: ctx, reader: body}, size)
	if err == nil {
		err = ctx.Err()
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

func remoteUploadSize(client *sftp.Client, remotePath string, resumed bool) (int64, error) {
	info, err := client.Stat(remotePath)
	if err == nil {
		return info.Size(), nil
	}
	if resumed && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("stat remote staging file %q: %w", remotePath, err)
	}
	if resumed {
		return 0, fmt.Errorf("remote staging file %q disappeared: %w", remotePath, drive.ErrNotFound)
	}
	return 0, nil
}

func removeRemoteFile(client *sftp.Client, remotePath string) error {
	if err := client.Remove(remotePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale staging file %q: %w", remotePath, err)
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

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
