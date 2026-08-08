package upload

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
)

type uploadTarget struct {
	UploadName      string
	ReplaceExisting []drive.Entry
	AlreadyReplaced bool
}

func prepareUploadTarget(ctx context.Context, remote RemoteOps, parentID, name, fid, replaceUploadID string) (uploadTarget, error) {
	target := uploadTarget{UploadName: name}
	if !remote.CanWrite() {
		return target, nil
	}
	entries, err := remote.List(ctx, parentID)
	if err != nil {
		return target, err
	}
	tempName := TemporaryUploadName(name, fid)
	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		switch entry.Name {
		case name:
			if replaceUploadID != "" && entry.ID == replaceUploadID {
				target.AlreadyReplaced = true
				continue
			}
			target.ReplaceExisting = append(target.ReplaceExisting, entry)
		case tempName:
			if replaceUploadID != "" && entry.ID == replaceUploadID {
				continue
			}
			logging.L.InfofEvery("vfs.remove_stale_temp_upload", time.Second, "[VFS] removing stale temporary upload parent=%q name=%q id=%q size=%d", parentID, tempName, entry.ID, entry.Size)
			if err := remote.Remove(ctx, entry); err != nil {
				return target, err
			}
		}
	}
	if len(target.ReplaceExisting) > 0 {
		target.UploadName = tempName
	}
	return target, nil
}
func replaceUploadedFile(ctx context.Context, remote RemoteOps, uploaded drive.Entry, existing []drive.Entry, finalName string) error {
	for _, entry := range existing {
		logging.L.InfofEvery("vfs.remove_existing_after_upload", time.Second, "[VFS] removing existing file after replacement upload parent=%q name=%q id=%q size=%d", entry.ParentID, entry.Name, entry.ID, entry.Size)
		if err := remote.Remove(ctx, entry); err != nil {
			return err
		}
	}
	if err := remote.Rename(ctx, uploaded, finalName); err != nil {
		return err
	}
	return nil
}
func TemporaryUploadName(name, fid string) string {
	if fid == "" {
		fid = stagingFID(name)
	}
	return ".qrypt-upload-" + fid + "-" + name
}
func stagingFID(path string) string {
	path = strings.Trim(vfstypes.CleanVirtualPath(path), "/")
	if path == "" {
		return "root"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	return replacer.Replace(path)
}
func validateUploadedEntry(entry drive.Entry, name string, size int64) error {
	if entry.ID == "" {
		return fmt.Errorf("%w: vfs: upload returned empty entry id", drive.ErrInvalidInput)
	}
	if entry.Size != size {
		return fmt.Errorf("%w: vfs: upload returned size %d, expected %d", drive.ErrInvalidInput, entry.Size, size)
	}
	if entry.Name != "" && entry.Name != name {
		return fmt.Errorf("%w: vfs: upload returned name %q, expected %q", drive.ErrInvalidInput, entry.Name, name)
	}
	return nil
}
