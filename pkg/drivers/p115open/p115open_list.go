// Package p115open implements the 115 cloud drive driver on the official
// 115 open platform API (Bearer access_token + refresh_token).
//
// The refresh token is obtained by authorizing an app on open.115.com (for
// example by scanning a PKCE device-code QR with the 115 app). The driver
// auto-refreshes the access token and persists rotated tokens in the state
// store, so a static refresh_token in the config stays valid across restarts.
package p115open

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	if err := d.waitLimit(ctx); err != nil {
		return nil, err
	}
	dirID := d.resolveID(parentID)
	var entries []drive.Entry
	err := d.recordSDK(ctx, "list", map[string]any{"parent_id": dirID}, func() error {
		var err error
		entries, err = d.getFiles(ctx, dirID)
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: list %q: %v", parentID, err))
		return nil, err
	}
	return entries, nil
}

func (d *Driver) getFiles(ctx context.Context, dirID string) ([]drive.Entry, error) {
	const pageSize = 1000
	var entries []drive.Entry
	offset := int64(0)
	for {
		resp, err := d.cl.GetFiles(ctx, &sdk.GetFilesReq{
			CID:     dirID,
			Limit:   pageSize,
			Offset:  offset,
			ShowDir: true,
		})
		if err != nil {
			return nil, err
		}
		for i := range resp.Data {
			entries = append(entries, entryFromFile(resp.Data[i]))
		}
		if len(resp.Data) == 0 || len(entries) >= int(resp.Count) {
			break
		}
		offset += int64(len(resp.Data))
	}
	return entries, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID string, name string) (drive.Entry, error) {
	if err := d.waitLimit(ctx); err != nil {
		return drive.Entry{}, err
	}
	parentID = d.resolveID(parentID)
	var resp *sdk.MkdirResp
	err := d.recordSDK(ctx, "mkdir", map[string]any{"parent_id": parentID, "name": name}, func() error {
		var err error
		resp, err = d.cl.Mkdir(ctx, parentID, name)
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: mkdir %q: %v", name, err))
		return drive.Entry{}, err
	}
	if resp == nil || resp.FileID == "" {
		return drive.Entry{}, fmt.Errorf("115_open: mkdir %q: empty response", name)
	}
	return drive.Entry{ID: resp.FileID, ParentID: parentID, Name: name, IsDir: true}, nil
}

// Copy implements drive.ServerSideCopier: copies the file server-side
// via the 115 open platform API, then renames it in the destination when
// the caller requests a different name (the Copy API keeps the source
// name). The final entry is located by listing the target directory.
func (d *Driver) Copy(ctx context.Context, src drive.Entry, dstParentID, dstName string) (drive.Entry, error) {
	if src.IsDir {
		return drive.Entry{}, drive.ErrUnsupported
	}
	if err := d.waitLimit(ctx); err != nil {
		return drive.Entry{}, err
	}
	dst := d.resolveID(dstParentID)
	err := d.recordSDK(ctx, "copy", map[string]any{"id": src.ID, "dst_parent_id": dst}, func() error {
		_, err := d.cl.Copy(ctx, &sdk.CopyReq{PID: dst, FileID: src.ID})
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: copy %q: %v", src.ID, err))
		return drive.Entry{}, err
	}
	if src.Name != dstName {
		entries, err := d.List(ctx, dstParentID)
		if err != nil {
			return drive.Entry{}, fmt.Errorf("115_open: copy locate after copy: %w", err)
		}
		for _, e := range entries {
			if e.Name == src.Name && e.ParentID == dstParentID {
				if _, err := d.cl.UpdateFile(ctx, &sdk.UpdateFileReq{FileID: e.ID, FileName: dstName}); err != nil {
					return drive.Entry{}, fmt.Errorf("115_open: copy rename %q -> %q: %w", e.ID, dstName, err)
				}
				return drive.Entry{ID: e.ID, ParentID: dstParentID, Name: dstName, Size: e.Size, ModTime: time.Now()}, nil
			}
		}
		return drive.Entry{ParentID: dstParentID, Name: dstName, Size: src.Size, ModTime: time.Now()}, fmt.Errorf("115_open: copy rename: source file %q not found after copy", src.Name)
	}
	return drive.Entry{ParentID: dstParentID, Name: dstName, Size: src.Size, ModTime: time.Now()}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	if err := d.waitLimit(ctx); err != nil {
		return err
	}
	dstParentID = d.resolveID(dstParentID)
	err := d.recordSDK(ctx, "move", map[string]any{"id": entry.ID, "dst_parent_id": dstParentID}, func() error {
		_, err := d.cl.Move(ctx, &sdk.MoveReq{FileIDs: entry.ID, ToCid: dstParentID})
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: move %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	if err := d.waitLimit(ctx); err != nil {
		return err
	}
	err := d.recordSDK(ctx, "rename", map[string]any{"id": entry.ID, "new_name": newName}, func() error {
		_, err := d.cl.UpdateFile(ctx, &sdk.UpdateFileReq{FileID: entry.ID, FileName: newName})
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: rename %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	if err := d.waitLimit(ctx); err != nil {
		return err
	}
	err := d.recordSDK(ctx, "remove", map[string]any{"id": entry.ID, "is_dir": entry.IsDir}, func() error {
		_, err := d.cl.DelFile(ctx, &sdk.DelFileReq{FileIDs: entry.ID, ParentID: entry.ParentID})
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: remove %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) resolveID(fileID string) string {
	if fileID == "" || fileID == "0" || fileID == "/" {
		return d.rootID
	}
	return fileID
}

func (d *Driver) resolvePathFrom(ctx context.Context, rootID, p string) (string, error) {
	currentID := d.resolveID(rootID)
	p = strings.Trim(p, "/")
	if p == "" {
		return currentID, nil
	}
	for _, segment := range strings.Split(p, "/") {
		entries, err := d.List(ctx, currentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.IsDir && entry.Name == segment {
				currentID = entry.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%w: directory %q not found under %q", drive.ErrNotFound, segment, p)
		}
	}
	return currentID, nil
}
