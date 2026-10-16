package upload

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateStagingRecreatesRemovedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "upload")
	store, err := NewPendingStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.StagingDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	path, err := store.CreateStaging("recovered")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteStagingAt(path, []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "data" {
		t.Fatalf("staging = %q, err = %v", data, err)
	}
}
