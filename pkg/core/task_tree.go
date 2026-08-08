package core

import (
	"context"
	"path"
	"sort"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

type taskTreeDir struct {
	Path string
	Rel  string
}

type taskTreeFile struct {
	Path  string
	Rel   string
	Entry drive.Entry
}

type taskTree struct {
	Root  drive.Entry
	Dirs  []taskTreeDir
	Files []taskTreeFile
}

func walkTaskTree(ctx context.Context, fs vfs.Reader, rootPath string) (taskTree, error) {
	rootPath = vfs.CleanVirtualPath(rootPath)
	root, err := fs.Stat(ctx, rootPath)
	if err != nil {
		return taskTree{}, err
	}
	tree := taskTree{Root: root}
	if !root.IsDir {
		tree.Files = append(tree.Files, taskTreeFile{Path: rootPath, Rel: path.Base(rootPath), Entry: root})
		return tree, nil
	}
	if err := walkTaskTreeDir(ctx, fs, rootPath, ".", &tree); err != nil {
		return taskTree{}, err
	}
	return tree, nil
}

func walkTaskTreeDir(ctx context.Context, fs vfs.Reader, dirPath, rel string, tree *taskTree) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tree.Dirs = append(tree.Dirs, taskTreeDir{Path: dirPath, Rel: rel})
	children, err := fs.List(vfs.WithoutDirPrefetch(ctx), dirPath)
	if err != nil {
		return err
	}
	sort.Slice(children, func(i, j int) bool {
		return children[i].Name < children[j].Name
	})
	for _, child := range children {
		childPath := path.Join(dirPath, child.Name)
		childRel := child.Name
		if rel != "." {
			childRel = path.Join(rel, child.Name)
		}
		if child.IsDir {
			if err := walkTaskTreeDir(ctx, fs, childPath, childRel, tree); err != nil {
				return err
			}
			continue
		}
		tree.Files = append(tree.Files, taskTreeFile{Path: childPath, Rel: childRel, Entry: child})
	}
	return nil
}

func taskTreeDirsDeepestFirst(dirs []taskTreeDir) []taskTreeDir {
	out := append([]taskTreeDir(nil), dirs...)
	sort.SliceStable(out, func(i, j int) bool {
		return taskTreeRelDepth(out[i].Rel) > taskTreeRelDepth(out[j].Rel)
	})
	return out
}

func taskTreeRelDepth(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

func relWithoutDot(rel string) string {
	if rel == "." {
		return ""
	}
	return rel
}
