package vfs

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSDriverRuntimeOwnsCapabilitiesAndBackends(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{Name: "mount", StorageDir: t.TempDir(), TestEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDriverRuntime(fs.driver, fs.testEnabled)

	if !runtime.HasCapability(drive.CapabilityWriter) {
		t.Fatal("localfs should report writer capability")
	}
	if err := runtime.RequireCapability(drive.CapabilityWriter, "write"); err != nil {
		t.Fatalf("RequireCapability returned error: %v", err)
	}
	runtime.MutationBackend()
	if backend := runtime.RemoteMutationBackend(); !backend.CanWrite() {
		t.Fatal("driver runtime should construct driver-backed backends")
	}
	named := runtime.NamedDriver("debug")
	if named.Name != "debug" || named.Driver == nil || !named.TestEnabled {
		t.Fatalf("named driver = %+v", named)
	}
}
