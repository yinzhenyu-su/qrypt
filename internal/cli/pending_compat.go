package cli

import (
	"io"

	clifs "github.com/yinzhenyu/qrypt/internal/cli/fs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func printPendingVerbose(w io.Writer, pending []vfs.PendingUpload) {
	clifs.PrintPendingVerbose(w, pending)
}
