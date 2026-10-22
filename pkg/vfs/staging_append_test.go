package vfs

import (
	"context"
	"os"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

func TestAppendsToFrozenGeneration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		pending PendingUpload
		off     int64
		n       int
		want    bool
	}{
		{
			name:    "claimed frozen generation accepts bytes past its size",
			pending: PendingUpload{Size: 11, Frozen: true, UploadStarted: true},
			off:     11,
			n:       5,
			want:    true,
		},
		{
			name:    "a gap past the frozen size is still an append",
			pending: PendingUpload{Size: 11, Frozen: true, UploadStarted: true},
			off:     64,
			n:       5,
			want:    true,
		},
		{
			name:    "rewriting inside the published prefix rotates",
			pending: PendingUpload{Size: 11, Frozen: true, UploadStarted: true},
			off:     6,
			n:       5,
			want:    false,
		},
		{
			name:    "an unclaimed generation is revived in place, not appended to",
			pending: PendingUpload{Size: 11, Frozen: true, UploadStarted: false},
			off:     11,
			n:       5,
			want:    false,
		},
		{
			name:    "a mutable generation never takes the frozen path",
			pending: PendingUpload{Size: 11, Frozen: false, UploadStarted: true},
			off:     11,
			n:       5,
			want:    false,
		},
		{
			name:    "a recorded replacement candidate keeps the copy",
			pending: PendingUpload{Size: 11, Frozen: true, UploadStarted: true, ReplaceUpload: &vfstypes.UploadReplacement{ID: "cand"}},
			off:     11,
			n:       5,
			want:    false,
		},
		{
			name:    "an empty write is not an append",
			pending: PendingUpload{Size: 11, Frozen: true, UploadStarted: true},
			off:     11,
			n:       0,
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendsToFrozenGeneration(tc.pending, tc.off, tc.n); got != tc.want {
				t.Fatalf("appendsToFrozenGeneration(%+v, %d, %d) = %v, want %v", tc.pending, tc.off, tc.n, got, tc.want)
			}
		})
	}
}

// TestAppendToClaimedGenerationKeepsStagingFile covers the copy this change
// removes: a write past the claimed size must land in the same staging file,
// so a large append that outlives one upload cycle costs one write instead of
// one full-file copy per cycle.
func TestAppendToClaimedGenerationKeepsStagingFile(t *testing.T) {
	t.Parallel()
	fs := newStateTestVFS(t)
	ctx := context.Background()
	if _, err := fs.WriteAt(ctx, "/doc.txt", []byte("hello"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/doc.txt"); err != nil {
		t.Fatal(err)
	}
	frozen, ok := fs.uploads.Store().UploadByPath("/doc.txt")
	if !ok || !frozen.Frozen || frozen.UploadStarted {
		t.Fatalf("pending before claim = %+v ok=%v, want a frozen unclaimed generation", frozen, ok)
	}
	claimed, ok := fs.uploads.Store().MarkUploadStarted(frozen)
	if !ok {
		t.Fatal("claim rejected the frozen generation")
	}
	if _, err := fs.WriteAt(ctx, "/doc.txt", []byte(" world"), claimed.Size); err != nil {
		t.Fatal(err)
	}
	after, ok := fs.uploads.Store().UploadByPath("/doc.txt")
	if !ok {
		t.Fatal("pending disappeared after the append")
	}
	if after.LocalPath != claimed.LocalPath || after.FID != claimed.FID {
		t.Fatalf("append rotated the generation: local %q -> %q, fid %q -> %q", claimed.LocalPath, after.LocalPath, claimed.FID, after.FID)
	}
	data, err := os.ReadFile(after.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Fatalf("staging content = %q, want %q", data, "hello world")
	}
	if copies := fs.stagingCopies.Load(); copies != 0 {
		t.Fatalf("append copied the staging file %d times, want 0", copies)
	}
	if copied := fs.stagingCopyBytes.Load(); copied != 0 {
		t.Fatalf("append copied %d bytes, want 0", copied)
	}
}

// TestWriteInsideClaimedPrefixRotatesGeneration is the other half: a write that
// touches the published prefix must still rotate to a fresh staging file.
func TestWriteInsideClaimedPrefixRotatesGeneration(t *testing.T) {
	t.Parallel()
	fs := newStateTestVFS(t)
	ctx := context.Background()
	if _, err := fs.WriteAt(ctx, "/doc.txt", []byte("hello"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/doc.txt"); err != nil {
		t.Fatal(err)
	}
	frozen, ok := fs.uploads.Store().UploadByPath("/doc.txt")
	if !ok {
		t.Fatal("pending missing before claim")
	}
	claimed, ok := fs.uploads.Store().MarkUploadStarted(frozen)
	if !ok {
		t.Fatal("claim rejected the frozen generation")
	}
	if _, err := fs.WriteAt(ctx, "/doc.txt", []byte("HELLO"), 0); err != nil {
		t.Fatal(err)
	}
	after, ok := fs.uploads.Store().UploadByPath("/doc.txt")
	if !ok {
		t.Fatal("pending disappeared after the overwrite")
	}
	if after.LocalPath == claimed.LocalPath {
		t.Fatal("overwrite inside the published prefix reused the frozen staging file")
	}
	data, err := os.ReadFile(after.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "HELLO" {
		t.Fatalf("rotated content = %q, want %q", data, "HELLO")
	}
	if _, err := os.Stat(claimed.LocalPath); err != nil {
		t.Fatalf("frozen staging must survive rotation: %v", err)
	}
	if copies := fs.stagingCopies.Load(); copies != 1 {
		t.Fatalf("prefix rewrite copies = %d, want 1", copies)
	}
	if copied := fs.stagingCopyBytes.Load(); copied != uint64(claimed.Size) {
		t.Fatalf("bytes copied = %d, want %d", copied, claimed.Size)
	}

	// A second rotation inside the log throttle window still has to be counted:
	// the emitted line is rate limited, the totals are not.
	if err := fs.Flush(ctx, "/doc.txt"); err != nil {
		t.Fatal(err)
	}
	frozen2, ok := fs.uploads.Store().UploadByPath("/doc.txt")
	if !ok || !frozen2.Frozen {
		t.Fatalf("pending before second claim = %+v ok=%v, want a frozen generation", frozen2, ok)
	}
	claimed2, ok := fs.uploads.Store().MarkUploadStarted(frozen2)
	if !ok {
		t.Fatal("second claim rejected the frozen generation")
	}
	if _, err := fs.WriteAt(ctx, "/doc.txt", []byte("HI"), 0); err != nil {
		t.Fatal(err)
	}
	if copies := fs.stagingCopies.Load(); copies != 2 {
		t.Fatalf("copies after second rewrite = %d, want 2", copies)
	}
	if copied := fs.stagingCopyBytes.Load(); copied != uint64(claimed.Size+claimed2.Size) {
		t.Fatalf("bytes copied after second rewrite = %d, want %d", copied, claimed.Size+claimed2.Size)
	}
}
