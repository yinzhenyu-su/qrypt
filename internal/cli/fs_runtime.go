package cli

import (
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

// builtFS is a filesystem the CLI constructs and starts. Every builder path
// returns a value with the full file-operation surface plus the lifecycle
// hook, so commands can start it without type assertions.
type builtFS interface {
	vfs.FileSystem
	vfs.Lifecycle
	vfs.UploadInspector
	diagnostics.DebugSnapshotProvider
}
