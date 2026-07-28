package vfs

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type recordingRemoteMutationBackend struct {
	canWrite bool
	entries  []drive.Entry
	removed  []string
	renamed  []string
}

func (b *recordingRemoteMutationBackend) CanWrite() bool {
	return b.canWrite
}

func (b *recordingRemoteMutationBackend) List(context.Context, string) ([]drive.Entry, error) {
	return append([]drive.Entry(nil), b.entries...), nil
}

func (b *recordingRemoteMutationBackend) PutSource(context.Context, drive.UploadRequest) (drive.Entry, error) {
	return drive.Entry{}, nil
}

func (b *recordingRemoteMutationBackend) Remove(_ context.Context, entry drive.Entry) error {
	b.removed = append(b.removed, entry.ID)
	return nil
}

func (b *recordingRemoteMutationBackend) Rename(_ context.Context, entry drive.Entry, newName string) error {
	b.renamed = append(b.renamed, entry.ID+":"+newName)
	return nil
}

func TestPrepareUploadTargetUsesRemoteMutationBackend(t *testing.T) {
	remote := &recordingRemoteMutationBackend{
		canWrite: true,
		entries: []drive.Entry{
			{ID: "existing", ParentID: "root", Name: "file.txt", Size: 10},
			{ID: "stale-temp", ParentID: "root", Name: temporaryUploadName("file.txt", "fid"), Size: 3},
		},
	}
	target, err := prepareUploadTarget(context.Background(), remote, "root", "file.txt", "fid", "")
	if err != nil {
		t.Fatal(err)
	}
	if target.UploadName != temporaryUploadName("file.txt", "fid") || len(target.ReplaceExisting) != 1 || target.ReplaceExisting[0].ID != "existing" {
		t.Fatalf("target = %+v, want temp replacement target", target)
	}
	if len(remote.removed) != 1 || remote.removed[0] != "stale-temp" {
		t.Fatalf("removed = %+v, want stale temp", remote.removed)
	}
}

func TestReplaceUploadedFileUsesRemoteMutationBackend(t *testing.T) {
	remote := &recordingRemoteMutationBackend{canWrite: true}
	err := replaceUploadedFile(context.Background(), remote, drive.Entry{ID: "uploaded"}, []drive.Entry{{ID: "old-1"}, {ID: "old-2"}}, "final.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(remote.removed) != 2 || remote.removed[0] != "old-1" || remote.removed[1] != "old-2" {
		t.Fatalf("removed = %+v, want old files", remote.removed)
	}
	if len(remote.renamed) != 1 || remote.renamed[0] != "uploaded:final.txt" {
		t.Fatalf("renamed = %+v, want uploaded rename", remote.renamed)
	}
}
