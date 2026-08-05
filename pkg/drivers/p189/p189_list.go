package p189

import (
	"context"
	"fmt"
	"strconv"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	id, err := strconv.ParseInt(parentID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("189: invalid id: %w", err)
	}
	folders, files, err := d.cl.listFiles(ctx, id)
	if err != nil {
		return nil, err
	}
	entries := make([]drive.Entry, 0, len(folders)+len(files))
	for _, f := range folders {
		createdAt := parseTime(f.CreateDate)
		modTime := parseTime(f.LastOpTime)
		entries = append(entries, drive.Entry{
			ID:        strconv.FormatInt(f.ID, 10),
			ParentID:  parentID,
			Name:      f.Name,
			IsDir:     true,
			ModTime:   modTime,
			CreatedAt: createdAt,
			UpdatedAt: modTime,
		})
	}
	for _, f := range files {
		createdAt := parseTime(f.CreateDate)
		modTime := parseTime(f.LastOpTime)
		entries = append(entries, drive.Entry{
			ID:        strconv.FormatInt(f.ID, 10),
			ParentID:  parentID,
			Name:      f.Name,
			Size:      f.Size,
			ModTime:   modTime,
			CreatedAt: createdAt,
			UpdatedAt: modTime,
		})
	}
	return entries, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID string, name string) (drive.Entry, error) {
	parent, err := strconv.ParseInt(parentID, 10, 64)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("189: invalid parent id: %w", err)
	}
	id, err := d.cl.createFolder(ctx, parent, name)
	if err != nil {
		return drive.Entry{}, err
	}
	return drive.Entry{
		ID:       strconv.FormatInt(id, 10),
		ParentID: parentID,
		Name:     name,
		IsDir:    true,
	}, nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	id, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("189: invalid id: %w", err)
	}
	isFolder := 0
	if entry.IsDir {
		isFolder = 1
	}
	taskInfos, err := batchTaskInfos(batchTaskInfo{FileID: id, FileName: entry.Name, IsFolder: isFolder})
	if err != nil {
		return err
	}
	return d.cl.batchTask(ctx, "DELETE", taskInfos, "")
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	id, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("189: invalid id: %w", err)
	}
	return d.cl.rename(ctx, id, newName, entry.IsDir)
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	id, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("189: invalid id: %w", err)
	}
	isFolder := 0
	if entry.IsDir {
		isFolder = 1
	}
	taskInfos, err := batchTaskInfos(batchTaskInfo{FileID: id, FileName: entry.Name, IsFolder: isFolder})
	if err != nil {
		return err
	}
	return d.cl.batchTask(ctx, "MOVE", taskInfos, dstParentID)
}
