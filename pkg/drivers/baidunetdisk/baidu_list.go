package baidunetdisk

import (
	"context"
	"fmt"
	"path"
	"strconv"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	dir := d.resolvePath(parentID)
	return d.listDir(ctx, dir)
}

func (d *Driver) listDir(ctx context.Context, dir string) ([]drive.Entry, error) {
	start := 0
	entries := make([]drive.Entry, 0)
	for {
		query := map[string]string{
			"method": "list",
			"dir":    dir,
			"web":    "web",
			"start":  strconv.Itoa(start),
			"limit":  strconv.Itoa(defaultListPageLimit),
		}
		if d.orderBy != "" {
			query["order"] = d.orderBy
			if d.orderDesc {
				query["desc"] = "1"
			}
		}
		var resp listResp
		if err := d.get(ctx, "/xpan/file", query, &resp); err != nil {
			err = fmt.Errorf("baidu_netdisk: list %q: %w", dir, err)
			d.setLastError(err)
			return nil, err
		}
		for _, item := range resp.List {
			entries = append(entries, item.entry(dir))
		}
		if len(resp.List) < defaultListPageLimit {
			break
		}
		start += defaultListPageLimit
	}
	return entries, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	parentPath := d.resolvePath(parentID)
	newPath := path.Join(parentPath, name)
	var resp createResp
	if err := d.create(ctx, newPath, 0, 1, &resp); err != nil {
		err = fmt.Errorf("baidu_netdisk: mkdir %q: %w", newPath, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	entry := drive.Entry{ID: newPath, ParentID: parentPath, Name: name, IsDir: true}
	if resp.File.Path != "" {
		entry = resp.File.entry(parentPath)
	} else if resp.Path != "" {
		entry.ID = resp.Path
	}
	if resp.FsID > 0 {
		entry.Extra = map[string]any{"fs_id": strconv.FormatInt(resp.FsID, 10)}
	}
	return entry, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	dst := d.resolvePath(dstParentID)
	err := d.manage(ctx, "move", []map[string]string{{"path": entry.ID, "dest": dst, "newname": entry.Name}})
	if err != nil {
		err = fmt.Errorf("baidu_netdisk: move %q to %q: %w", entry.ID, dst, err)
		d.setLastError(err)
	}
	return err
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	err := d.manage(ctx, "rename", []map[string]string{{"path": entry.ID, "newname": newName}})
	if err != nil {
		err = fmt.Errorf("baidu_netdisk: rename %q: %w", entry.ID, err)
		d.setLastError(err)
	}
	return err
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	err := d.manage(ctx, "delete", []string{entry.ID})
	if err != nil {
		err = fmt.Errorf("baidu_netdisk: remove %q: %w", entry.ID, err)
		d.setLastError(err)
	}
	return err
}

func (d *Driver) create(ctx context.Context, p string, size int64, isDir int, out any) error {
	return d.postForm(ctx, "/xpan/file", map[string]string{"method": "create"}, map[string]string{
		"path":  p,
		"size":  strconv.FormatInt(size, 10),
		"isdir": strconv.Itoa(isDir),
		"rtype": "3",
	}, out)
}

func (d *Driver) createFile(ctx context.Context, p string, size int64, uploadID, blockList string, out any) error {
	return d.postForm(ctx, "/xpan/file", map[string]string{"method": "create"}, map[string]string{
		"path":       p,
		"size":       strconv.FormatInt(size, 10),
		"isdir":      "0",
		"rtype":      "3",
		"uploadid":   uploadID,
		"block_list": blockList,
	}, out)
}
