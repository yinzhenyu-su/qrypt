package sync

import (
	"reflect"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/fsclient"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// The executor probes destinations for optional write capabilities and skips
// the corresponding step when a filesystem does not implement them. That is
// right for external drivers, but it once silently degraded fs sync: the CLI
// reached the Core through fsclient.FS, which lacked vfs.ModTimeWriter while
// the in-process VFS had it, so destination mtimes never converged and every
// sync re-uploaded files whose source was older than the destination.
//
// These assertions keep the in-process implementations and the Core client in
// lockstep. When the executor starts probing another optional interface, add
// it to both the compile-time guards and TestSyncWriteCapabilitiesMatch.
var (
	_ vfs.ModTimeWriter = (*vfs.VFS)(nil)
	_ vfs.ModTimeWriter = (*vfs.Namespace)(nil)
	_ vfs.ModTimeWriter = (*fsclient.FS)(nil)
)

// TestSyncWriteCapabilitiesMatch checks the same property through a table so
// a missing capability reports which filesystem lost it, instead of failing
// at some unrelated sync. Keep the probed list aligned with the optional
// assertions inside the executor (currently syncer.SetModTime).
func TestSyncWriteCapabilitiesMatch(t *testing.T) {
	probed := []struct {
		name  string
		iface reflect.Type
	}{
		{"vfs.ModTimeWriter", reflect.TypeOf((*vfs.ModTimeWriter)(nil)).Elem()},
	}
	implementations := []struct {
		name string
		fs   any
	}{
		{"in-process VFS", (*vfs.VFS)(nil)},
		{"in-process Namespace", (*vfs.Namespace)(nil)},
		{"Core data-plane client", (*fsclient.FS)(nil)},
	}

	for _, capability := range probed {
		for _, impl := range implementations {
			implType := reflect.TypeOf(impl.fs)
			if implType == nil {
				t.Fatalf("%s: missing implementation value", impl.name)
			}
			if !implType.Implements(capability.iface) {
				t.Errorf("%s does not implement %s, so sync silently skips that capability for this filesystem", impl.name, capability.name)
			}
		}
	}
}
