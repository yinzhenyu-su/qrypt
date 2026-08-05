// Package p115 implements the 115 cloud drive driver.
package p115

import (
	"context"
	"fmt"
	"time"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	if err := d.waitLimit(ctx); err != nil {
		return nil, err
	}
	var entries []drive.Entry
	err := d.recordSDK(ctx, "list", map[string]any{"parent_id": d.resolveID(parentID)}, func() error {
		var err error
		entries, err = d.getFiles(d.resolveID(parentID))
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: list %q: %v", parentID, err))
		return nil, err
	}
	return entries, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID string, name string) (drive.Entry, error) {
	parentID = d.resolveID(parentID)
	var id string
	err := d.recordSDK(ctx, "mkdir", map[string]any{"parent_id": parentID, "name": name}, func() error {
		var err error
		id, err = d.cl.Mkdir(parentID, name)
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: mkdir %q: %v", name, err))
		return drive.Entry{}, err
	}
	entry, err := d.getNewEntry(ctx, id)
	if err == nil {
		return entry, nil
	}
	return drive.Entry{ID: id, ParentID: parentID, Name: name, IsDir: true}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	dstParentID = d.resolveID(dstParentID)
	err := d.recordSDK(ctx, "move", map[string]any{"id": entry.ID, "dst_parent_id": dstParentID}, func() error {
		return d.cl.Move(dstParentID, entry.ID)
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: move %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	err := d.recordSDK(ctx, "rename", map[string]any{"id": entry.ID, "new_name": newName}, func() error {
		return d.cl.Rename(entry.ID, newName)
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: rename %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	err := d.recordSDK(ctx, "remove", map[string]any{"id": entry.ID, "is_dir": entry.IsDir}, func() error {
		return d.removeWithRetry(ctx, entry.ID)
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: remove %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) removeWithRetry(ctx context.Context, id string) error {
	var err error
	for attempt := 0; attempt < 7; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		err = d.cl.Delete(id)
		if err == nil || !isPendingDeleteError(err) {
			return err
		}
	}
	return err
}

func (d *Driver) getFiles(dirID string) ([]drive.Entry, error) {
	files, err := d.cl.ListWithLimit(dirID, 1000, driver115.WithMultiUrls())
	if err != nil {
		return nil, err
	}
	entries := make([]drive.Entry, len(*files))
	for i, f := range *files {
		modTime := f.ModTime()
		entries[i] = drive.Entry{
			ID:        f.GetID(),
			ParentID:  f.ParentID,
			Name:      f.GetName(),
			Size:      f.GetSize(),
			IsDir:     f.IsDir(),
			ModTime:   modTime,
			UpdatedAt: modTime,
			Extra:     f,
		}
	}
	return entries, nil
}

func (d *Driver) getNewEntry(ctx context.Context, id string) (drive.Entry, error) {
	var f *driver115.File
	err := d.recordSDK(ctx, "get_file", map[string]any{"id": id}, func() error {
		var err error
		f, err = d.cl.GetFile(id)
		return err
	})
	if err != nil {
		return drive.Entry{}, err
	}
	return entryFromFile(*f), nil
}
