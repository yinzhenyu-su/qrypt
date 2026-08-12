package cli

import (
	clifs "github.com/yinzhenyu/qrypt/internal/cli/fs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/drivecopy"
)

type fsCopyDryRunResult = clifs.CopyDryRunResult

func fsCopyDirError(result *drivecopy.DriverCopyDirResult) error {
	return clifs.CopyDirError(cliRuntime{}, result)
}
