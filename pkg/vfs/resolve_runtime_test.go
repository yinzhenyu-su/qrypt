package vfs

import (
	"context"
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type fakeResolveRuntime struct {
	cached      map[string]drive.Entry
	unavailable map[string]bool
	recentLocal map[string]bool
	committed   []drive.Entry
}

func (r *fakeResolveRuntime) CachedEntry(path string) (drive.Entry, bool) {
	entry, ok := r.cached[cleanVirtual(path)]
	return entry, ok
}

func (r *fakeResolveRuntime) CommitResolvedChildren(_ string, name string, entries []drive.Entry) (drive.Entry, bool) {
	r.committed = cloneEntries(entries)
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return drive.Entry{}, false
}

func (r *fakeResolveRuntime) IsUnavailable(path string) bool {
	return r.unavailable[cleanVirtual(path)]
}

func (r *fakeResolveRuntime) IsRecentLocalDir(path string) bool {
	return r.recentLocal[cleanVirtual(path)]
}

func TestResolveWithRuntimeReturnsCachedEntry(t *testing.T) {
	runtime := &fakeResolveRuntime{
		cached: map[string]drive.Entry{"/file.txt": {ID: "file", Name: "file.txt"}},
	}
	entry, err := resolveWithRuntime(context.Background(), "/file.txt", runtime, func(context.Context, string) (drive.Entry, error) {
		t.Fatal("parent resolver should not be called")
		return drive.Entry{}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "file" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestResolveWithRuntimeListsParentAndCommitsChildren(t *testing.T) {
	runtime := &fakeResolveRuntime{}
	entry, err := resolveWithRuntime(context.Background(), "/dir/file.txt", runtime,
		func(context.Context, string) (drive.Entry, error) {
			return drive.Entry{ID: "dir", Name: "dir", IsDir: true}, nil
		},
		func(_ context.Context, parentPath, parentID string) ([]drive.Entry, error) {
			if parentPath != "/dir" || parentID != "dir" {
				t.Fatalf("list parentPath=%q parentID=%q", parentPath, parentID)
			}
			return []drive.Entry{{ID: "file", Name: "file.txt"}}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "file" || len(runtime.committed) != 1 {
		t.Fatalf("entry=%+v committed=%+v", entry, runtime.committed)
	}
}

func TestResolveWithRuntimeSkipsRecentLocalDir(t *testing.T) {
	runtime := &fakeResolveRuntime{recentLocal: map[string]bool{"/dir": true}}
	_, err := resolveWithRuntime(context.Background(), "/dir/file.txt", runtime,
		func(context.Context, string) (drive.Entry, error) {
			return drive.Entry{ID: "dir", Name: "dir", IsDir: true}, nil
		},
		func(context.Context, string, string) ([]drive.Entry, error) {
			t.Fatal("list children should not be called")
			return nil, nil
		},
	)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
