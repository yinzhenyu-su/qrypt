package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// treeEntry is one node of a directory snapshot. Directories are kept as
// entries so type mismatches (file vs directory on the other side) can be
// detected instead of silently overwritten.
type treeEntry struct {
	RelPath string
	IsDir   bool
	Size    int64
	ModTime int64 // unix seconds
	Entry   *drive.Entry
}

// treeSnapshot maps relative slash-separated paths to entries.
type treeSnapshot map[string]treeEntry

// fileCount returns the number of file (non-directory) entries.
func (s treeSnapshot) fileCount() int {
	n := 0
	for _, entry := range s {
		if !entry.IsDir {
			n++
		}
	}
	return n
}

// snapshotVFS lists every entry under root (files and directories), keyed by
// slash-separated relative path. The root itself is not included.
func snapshotVFS(ctx context.Context, fs vfs.FileSystem, root string) (treeSnapshot, error) {
	result := treeSnapshot{}
	var walk func(dir, prefix string) error
	walk = func(dir, prefix string) error {
		entries, err := fs.List(ctx, dir)
		if err != nil {
			return err
		}
		for i := range entries {
			entry := entries[i]
			rel := pathpkg.Join(prefix, entry.Name)
			te := treeEntry{RelPath: rel, IsDir: entry.IsDir}
			if !entry.IsDir {
				te.Size = entry.Size
				te.ModTime = entry.ModTime.Unix()
				te.Entry = &entry
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

// snapshotLocal lists every entry under root (files and directories), keyed
// by slash-separated relative path. The root itself is not included.
func snapshotLocal(root string) (treeSnapshot, error) {
	result := treeSnapshot{}
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
		te := treeEntry{RelPath: filepath.ToSlash(rel), IsDir: entry.IsDir()}
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

// treeCompareOptions configures the comparison. Hash is called for entries
// whose size and mtime match, when the caller wants content verification.
type treeCompareOptions struct {
	AsHash bool
	// Hash reports whether the file at the relative path has the same
	// content on both sides. detail describes the remote hash on mismatch.
	Hash func(ctx context.Context, rel string) (matched bool, detail string, err error)
}

// treeDifference records one mismatch between two tree snapshots. Reasons:
// missing_in_b, extra_in_b, size, mtime, hash, type. Direction is relative
// to the argument order: source is A, destination is B.
type treeDifference struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	IsDir  bool   `json:"is_dir,omitempty"`
	A      string `json:"a,omitempty"`
	B      string `json:"b,omitempty"`
}

// compareTrees compares two snapshots. Directories are compared for
// presence and type only; files for size, mtime, and optionally hash.
func compareTrees(ctx context.Context, source, destination treeSnapshot, opts treeCompareOptions) ([]treeDifference, error) {
	var diffs []treeDifference
	for rel, entryA := range source {
		entryB, ok := destination[rel]
		if !ok {
			diffs = append(diffs, treeDifference{Path: rel, Reason: "missing_in_b", IsDir: entryA.IsDir})
			continue
		}
		if entryA.IsDir != entryB.IsDir {
			diffs = append(diffs, treeDifference{Path: rel, Reason: "type", IsDir: entryA.IsDir, A: entryKind(entryA), B: entryKind(entryB)})
			continue
		}
		if entryA.IsDir {
			continue // directories are present on both sides with matching type
		}
		if entryA.Size != entryB.Size {
			diffs = append(diffs, treeDifference{Path: rel, Reason: "size", A: fmt.Sprintf("%d", entryA.Size), B: fmt.Sprintf("%d", entryB.Size)})
			continue
		}
		if entryA.ModTime != entryB.ModTime {
			diffs = append(diffs, treeDifference{Path: rel, Reason: "mtime", A: fmt.Sprintf("%d", entryA.ModTime), B: fmt.Sprintf("%d", entryB.ModTime)})
			continue
		}
		if opts.AsHash && opts.Hash != nil {
			matched, detail, err := opts.Hash(ctx, rel)
			if err != nil {
				return diffs, err
			}
			if !matched {
				diffs = append(diffs, treeDifference{Path: rel, Reason: "hash", A: detail})
			}
		}
	}
	for rel, entryB := range destination {
		if _, ok := source[rel]; !ok {
			diffs = append(diffs, treeDifference{Path: rel, Reason: "extra_in_b", IsDir: entryB.IsDir})
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Path < diffs[j].Path })
	return diffs, nil
}

func entryKind(entry treeEntry) string {
	if entry.IsDir {
		return "dir"
	}
	return "file"
}

// snapshotTarget classifies and snapshots one check/sync target.
func snapshotTarget(ctx context.Context, fs vfs.FileSystem, target checkTarget) (treeSnapshot, error) {
	if target.kind == targetVFS {
		snap, err := snapshotVFS(ctx, fs, target.vfsPath)
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", target.raw, err)
		}
		return snap, nil
	}
	snap, err := snapshotLocal(target.localPath)
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", target.raw, err)
	}
	return snap, nil
}

// localTreeFile remains for compatibility with callers of the old walkers.
type localTreeFile struct {
	size    int64
	modTime int64
}

// walkLocalTree lists every file under root, keyed by slash-separated
// relative path. Retained for callers that need the legacy shape.
func walkLocalTree(root string) (map[string]localTreeFile, error) {
	snap, err := snapshotLocal(root)
	if err != nil {
		return nil, err
	}
	result := map[string]localTreeFile{}
	for rel, entry := range snap {
		if entry.IsDir {
			continue
		}
		result[rel] = localTreeFile{size: entry.Size, modTime: entry.ModTime}
	}
	return result, nil
}

// osPath converts a slash-separated relative path to a local path under root.
func osPath(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}

// localPathExists reports whether the local path exists and what kind it is.
func localPathExists(path string) (exists, isDir bool, err error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, info.IsDir(), nil
}
