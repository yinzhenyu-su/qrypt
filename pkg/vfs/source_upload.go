package vfs

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (v *VFS) SupportsSourceUpload(string) bool {
	return drive.HasCapability(v.driver, drive.CapabilitySourceUploader) &&
		drive.HasCapability(v.driver, drive.CapabilityWriter)
}

func (v *VFS) SupportsResumableSourceUpload(string) bool {
	return v.SupportsSourceUpload("") &&
		drive.HasCapability(v.driver, drive.CapabilityResumableUploader)
}

func (v *VFS) UploadSource(ctx context.Context, path string, req SourceUploadRequest) (entry drive.Entry, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpUpload, err) }()
	if !v.SupportsSourceUpload(path) {
		return drive.Entry{}, drive.ErrUnsupported
	}
	if req.Source == nil {
		return drive.Entry{}, fmt.Errorf("vfs: upload source required")
	}
	path = cleanVirtual(path)
	if path == "/" {
		return drive.Entry{}, ErrReadOnly
	}
	unlock := v.lockPath(path)
	defer unlock()
	parent, name, err := v.parent(ctx, path)
	if err != nil {
		return drive.Entry{}, err
	}
	resumable := v.SupportsResumableSourceUpload(path)
	if !resumable {
		v.removeSourceUploadTemporaryFiles(ctx, parent.ID, name)
	}
	uploadName := name
	replaceExisting, err := v.sourceUploadExisting(ctx, parent.ID, name)
	if err != nil {
		return drive.Entry{}, err
	}
	if len(replaceExisting) > 0 || !resumable {
		uploadName = sourceUploadTemporaryName(name)
	}
	v.restoreDeletedAncestor(filepath.Dir(path))
	v.cancelDeletedFile(path)
	v.unhideCopyChild(filepath.Dir(path), name)
	entry, err = v.driver.PutSource(ctx, drive.UploadRequest{
		ParentID: parent.ID,
		Name:     uploadName,
		Source:   req.Source,
		Progress: req.Progress,
		ModTime:  req.ModTime,
	})
	if err != nil {
		if uploadName != name {
			v.removeSourceUploadNamedTemporary(ctx, parent.ID, uploadName)
		}
		return drive.Entry{}, err
	}
	if len(replaceExisting) > 0 || uploadName != name {
		replaced, replaceErr := v.replaceSourceUploadedEntry(ctx, entry, replaceExisting, name)
		if replaceErr != nil {
			_ = v.driver.Remove(context.WithoutCancel(ctx), entry)
			return drive.Entry{}, replaceErr
		}
		entry = replaced
	}
	newVFSViewCommitter(v).CommitUploadedEntry(path, entry, "")
	return entry, nil
}

func (v *VFS) sourceUploadExisting(ctx context.Context, parentID, name string) ([]drive.Entry, error) {
	entries, err := v.driver.List(ctx, parentID)
	if err != nil {
		return nil, err
	}
	var existing []drive.Entry
	for _, entry := range entries {
		if !entry.IsDir && entry.Name == name {
			existing = append(existing, entry)
		}
	}
	return existing, nil
}

func (v *VFS) replaceSourceUploadedEntry(ctx context.Context, entry drive.Entry, existing []drive.Entry, finalName string) (drive.Entry, error) {
	for _, old := range existing {
		if err := v.driver.Remove(ctx, old); err != nil {
			return drive.Entry{}, err
		}
	}
	if entry.Name != finalName {
		if err := v.driver.Rename(ctx, entry, finalName); err != nil {
			return drive.Entry{}, err
		}
		if refreshed, ok := v.findSourceUploadedEntry(ctx, entry.ParentID, finalName); ok {
			return refreshed, nil
		}
	}
	entry.Name = finalName
	return entry, nil
}

func (v *VFS) findSourceUploadedEntry(ctx context.Context, parentID, name string) (drive.Entry, bool) {
	entries, err := v.driver.List(ctx, parentID)
	if err != nil {
		return drive.Entry{}, false
	}
	for _, entry := range entries {
		if !entry.IsDir && entry.Name == name {
			return entry, true
		}
	}
	return drive.Entry{}, false
}

func (v *VFS) removeSourceUploadTemporaryFiles(ctx context.Context, parentID, finalName string) {
	entries, err := v.driver.List(ctx, parentID)
	if err != nil {
		return
	}
	prefix := sourceUploadTemporaryPrefix(finalName)
	for _, entry := range entries {
		if entry.IsDir || !strings.HasPrefix(entry.Name, prefix) {
			continue
		}
		_ = v.driver.Remove(context.WithoutCancel(ctx), entry)
	}
}

func (v *VFS) removeSourceUploadNamedTemporary(ctx context.Context, parentID, uploadName string) {
	entries, err := v.driver.List(context.WithoutCancel(ctx), parentID)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir && entry.Name == uploadName {
			_ = v.driver.Remove(context.WithoutCancel(ctx), entry)
			return
		}
	}
}

func sourceUploadTemporaryPrefix(name string) string {
	return name + ".qrypt-upload-"
}

func sourceUploadTemporaryName(name string) string {
	return sourceUploadTemporaryPrefix(name) + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (n *Namespace) SupportsSourceUpload(path string) bool {
	mount, rest, root, err := n.resolve(path)
	if err != nil || root || rest == "/" {
		return false
	}
	return mount.SupportsSourceUpload(rest)
}

func (n *Namespace) SupportsResumableSourceUpload(path string) bool {
	mount, rest, root, err := n.resolve(path)
	if err != nil || root || rest == "/" {
		return false
	}
	return mount.SupportsResumableSourceUpload(rest)
}

func (n *Namespace) UploadSource(ctx context.Context, path string, req SourceUploadRequest) (drive.Entry, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return drive.Entry{}, err
	}
	if root || rest == "/" {
		return drive.Entry{}, ErrReadOnly
	}
	return mount.UploadSource(ctx, rest, req)
}
