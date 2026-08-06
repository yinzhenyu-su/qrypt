package sync

import (
	"context"
	"io/fs"
	pathpkg "path"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// Entry is one node of a directory snapshot. Directories are kept as entries
// so type mismatches (file vs directory on the other side) can be detected
// instead of silently overwritten.
type Entry struct {
	RelPath string
	IsDir   bool
	Size    int64
	ModTime int64 // unix seconds
	Drive   *drive.Entry
}

// Snapshot maps relative slash-separated paths to entries.
type Snapshot map[string]Entry

// FileCount returns the number of file (non-directory) entries.
func (s Snapshot) FileCount() int {
	n := 0
	for _, entry := range s {
		if !entry.IsDir {
			n++
		}
	}
	return n
}

// SnapshotVFS lists every entry under root (files and directories), keyed by
// slash-separated relative path. The root itself is not included.
func SnapshotVFS(ctx context.Context, fs vfs.FileSystem, root string) (Snapshot, error) {
	result := Snapshot{}
	var walk func(dir, prefix string) error
	walk = func(dir, prefix string) error {
		entries, err := fs.List(ctx, dir)
		if err != nil {
			return err
		}
		for i := range entries {
			entry := entries[i]
			rel := pathpkg.Join(prefix, entry.Name)
			te := Entry{RelPath: rel, IsDir: entry.IsDir}
			if !entry.IsDir {
				te.Size = entry.Size
				te.ModTime = entry.ModTime.Unix()
				te.Drive = &entry
			}
			result[rel] = te
			if entry.IsDir {
				if err := walk(pathpkg.Join(dir, entry.Name), rel); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	return result, nil
}

// SnapshotLocal lists every entry under root (files and directories), keyed
// by slash-separated relative path. The root itself is not included.
func SnapshotLocal(root string) (Snapshot, error) {
	result := Snapshot{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		te := Entry{RelPath: filepath.ToSlash(rel), IsDir: entry.IsDir()}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			te.Size = info.Size()
			te.ModTime = info.ModTime().Unix()
		}
		result[filepath.ToSlash(rel)] = te
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// SnapshotTarget snapshots either side of a sync pair.
func SnapshotTarget(ctx context.Context, fs vfs.FileSystem, target Target) (Snapshot, error) {
	switch target.Kind {
	case TargetVFS:
		return SnapshotVFS(ctx, fs, target.VFSPath)
	default:
		return SnapshotLocal(target.LocalPath)
	}
}
