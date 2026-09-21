package crypt

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// backendEchoDriver mimics a real backend: it only ever sees encrypted names,
// echoes back the name it was given for a rename, and reports its own name for
// an object on a move.
type backendEchoDriver struct {
	writableRawDriver
	backendName string // the name the backend knows the object by
	renameName  string // the name a rename was asked to use
}

func (d *backendEchoDriver) Rename(_ context.Context, entry drive.Entry, newName string) (drive.Entry, error) {
	d.renameName = newName
	entry.Name = newName
	return entry, nil
}

func (d *backendEchoDriver) Move(_ context.Context, entry drive.Entry, dstParentID string) (drive.Entry, error) {
	entry.Name = d.backendName
	entry.ParentID = dstParentID
	return entry, nil
}

// The wrapper's return contract, held by PutSource / Mkdir / List: callers get
// entries named in their own vocabulary, with the backend name reachable
// through Extra. Rename and Move used to return the backend's entry verbatim,
// which leaked a ciphertext name into the view and surfaced in the mount as a
// file nobody could address.
func TestWrapperRenameAndMoveReturnPlaintextNames(t *testing.T) {
	t.Parallel()
	cp, err := NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	raw := &backendEchoDriver{}
	raw.backendName = cp.EncryptSegment("plain.txt")
	driver := NewDriver(raw, cp, DriverOptions{})
	ctx := context.Background()

	renamed, err := driver.Rename(ctx, drive.Entry{ID: "id-1", Name: "old.txt"}, "plain.txt")
	if err != nil {
		t.Fatal(err)
	}
	if raw.renameName == "plain.txt" {
		t.Fatal("the backend should be given the encrypted name, not the plaintext one")
	}
	if renamed.Name != "plain.txt" {
		t.Fatalf("Rename returned Name=%q, want the plaintext name", renamed.Name)
	}
	remoteName, ok := drive.EntryRemoteName(renamed)
	if !ok || remoteName != raw.renameName {
		t.Fatalf("Rename remote name = %q (ok=%v), want the encrypted name %q", remoteName, ok, raw.renameName)
	}

	moved, err := driver.Move(ctx, drive.Entry{ID: "id-2", Name: "plain.txt"}, "dst-parent")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Name != "plain.txt" {
		t.Fatalf("Move returned Name=%q, want the plaintext name", moved.Name)
	}
	if moved.ParentID != "dst-parent" {
		t.Fatalf("Move returned ParentID=%q, want dst-parent", moved.ParentID)
	}
	movedRemote, ok := drive.EntryRemoteName(moved)
	if !ok || movedRemote != raw.backendName {
		t.Fatalf("Move remote name = %q (ok=%v), want the backend name %q", movedRemote, ok, raw.backendName)
	}
}
