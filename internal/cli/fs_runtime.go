package cli

import (
	"fmt"
	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

// builtFS is a filesystem the CLI constructed and will start. Every builder
// path returns a value with the full file-operation surface plus the
// lifecycle hook, so commands can Start it without type assertions. It is
// wider than cliruntime.OpenedFileSystem: the upload and debug-observation
// surfaces it carries let openFileSystem hand the started filesystem to
// commands fully typed (no any-assertions in the idle-wait and pending
// paths).
type builtFS interface {
	vfs.FileSystem
	vfs.Lifecycle
	vfs.UploadInspector
	diagnostics.DebugSnapshotProvider
}

// bandwidthOverrideFromFlags maps the fs --bwlimit flags onto bandwidth
// limits. nil means no override (use the config [bandwidth] section).
func bandwidthOverrideFromFlags(cmd *cobra.Command) (*config.BandwidthLimits, error) {
	both := cliruntime.FlagStringValue(cmd, "bwlimit")
	download := cliruntime.FlagStringValue(cmd, "bwlimit-download")
	upload := cliruntime.FlagStringValue(cmd, "bwlimit-upload")
	if both == "" && download == "" && upload == "" {
		return nil, nil
	}
	parse := func(flagName, value string) (int64, error) {
		if value == "" {
			return 0, nil
		}
		n, err := config.ParseSize(value)
		if err != nil {
			return 0, fmt.Errorf("invalid %s: %w", flagName, err)
		}
		return n, nil
	}
	limits := &config.BandwidthLimits{}
	if both != "" {
		n, err := parse("--bwlimit", both)
		if err != nil {
			return nil, err
		}
		limits.DownloadBytesPerSecond = n
		limits.UploadBytesPerSecond = n
	}
	if download != "" {
		down, err := parse("--bwlimit-download", download)
		if err != nil {
			return nil, err
		}
		limits.DownloadBytesPerSecond = down
	}
	if upload != "" {
		up, err := parse("--bwlimit-upload", upload)
		if err != nil {
			return nil, err
		}
		limits.UploadBytesPerSecond = up
	}
	if limits.DownloadBytesPerSecond <= 0 && limits.UploadBytesPerSecond <= 0 {
		return nil, fmt.Errorf("bandwidth limit must be greater than zero")
	}
	return limits, nil
}

func addFSBandwidthFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String("bwlimit", "", "limit both download and upload bandwidth (e.g. 10M)")
	cmd.PersistentFlags().String("bwlimit-download", "", "limit download bandwidth (e.g. 10M)")
	cmd.PersistentFlags().String("bwlimit-upload", "", "limit upload bandwidth (e.g. 10M)")
	for _, name := range []string{"bwlimit", "bwlimit-download", "bwlimit-upload"} {
		cliruntime.MarkAdvancedFlag(cmd.PersistentFlags().Lookup(name))
	}
}
