package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/mount"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type ConfigState struct {
	Path string
	Cfg  *config.Config
}

const (
	ExitPartial  = 3
	ExitMismatch = 4
)

type FileSystem interface {
	vfs.FileSystem
}

type MountFileSystem interface {
	vfs.FileSystem
	vfs.Lifecycle
}

type Runtime interface {
	BuildFileSystemForMount(ctx context.Context, cfg *config.Config, mountName string) (MountFileSystem, func(), error)
	CommandConfig(cmd *cobra.Command) (ConfigState, error)
	CommandConfigPath(cmd *cobra.Command) (string, error)
	ConfigNotFoundError() error
	DefaultCacheDir() string
	DebugReportSchemaVersion() int
	ExitError(code int, err error) error
	MissingSocketError(cmd *cobra.Command) error
	MountOptionsFromConfig(cfg *config.Config) (mount.Options, error)
	MountPointFromConfig(cfg *config.Config) (string, error)
	OpenFileSystem(cmd *cobra.Command) (context.Context, FileSystem, func(), error)
	ShutdownContext(cmd *cobra.Command) (context.Context, func())
	UsageError(cmd *cobra.Command, format string, args ...any) error
	WaitFileSystemIdle(ctx context.Context, fs any, timeout time.Duration) error
	WithFSBandwidthFlags(cmd *cobra.Command) *cobra.Command
	WithConfigFlag(cmd *cobra.Command) *cobra.Command
	WithPersistentConfigFlag(cmd *cobra.Command) *cobra.Command
	WithPersistentRuntimeConfigFlag(cmd *cobra.Command) *cobra.Command
	WithRuntimeConfigFlag(cmd *cobra.Command) *cobra.Command
}

func CommandGroupArgs(rt Runtime, hints map[string]string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		if hint := hints[args[0]]; hint != "" {
			return rt.UsageError(cmd, "%s", hint)
		}
		return rt.UsageError(cmd, "unknown command %q for %q\n\nRun '%s --help' to see available commands.", args[0], cmd.CommandPath(), cmd.CommandPath())
	}
}

func RangeArgs(rt Runtime, minArgs, maxArgs int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= minArgs && len(args) <= maxArgs {
			return nil
		}
		if len(args) < minArgs {
			return rt.UsageError(cmd, "missing arguments: expected %d to %d, got %d", minArgs, maxArgs, len(args))
		}
		return rt.UsageError(cmd, "too many arguments: expected %d to %d, got %d", minArgs, maxArgs, len(args))
	}
}

func NoArgs(rt Runtime) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != 0 {
			return rt.UsageError(cmd, "unexpected argument %q", args[0])
		}
		return nil
	}
}

func MaxArgs(rt Runtime, n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= n {
			return nil
		}
		return rt.UsageError(cmd, "too many arguments: %s", strings.Join(args[n:], " "))
	}
}

func ExactNamedArgs(rt Runtime, names ...string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == len(names) {
			return nil
		}
		switch {
		case len(args) < len(names):
			return rt.UsageError(cmd, "missing %s", strings.Join(names[len(args):], " and "))
		default:
			return rt.UsageError(cmd, "too many arguments: %s", strings.Join(args[len(names):], " "))
		}
	}
}

func NoFileCompletions(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func NonNegativeIntFlag(cmd *cobra.Command, name string) (int, error) {
	value, err := cmd.Flags().GetInt(name)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, fmt.Errorf("--%s must not be negative", name)
	}
	return value, nil
}

func ValidateSamplingWindow(duration, interval time.Duration, durationFlag, intervalFlag string) error {
	if duration <= 0 {
		return fmt.Errorf("--%s must be greater than 0", durationFlag)
	}
	if interval <= 0 {
		return fmt.Errorf("--%s must be greater than 0", intervalFlag)
	}
	if interval > duration {
		return fmt.Errorf("--%s must not exceed --%s", intervalFlag, durationFlag)
	}
	return nil
}

func ShowHelp(cmd *cobra.Command, _ []string) error {
	return cmd.Help()
}

func CommandWaitTimeout(cmd *cobra.Command) time.Duration {
	if cmd.Flag("wait-timeout") == nil {
		return 30 * time.Second
	}
	timeout, _ := cmd.Flags().GetDuration("wait-timeout")
	return timeout
}

func PrintEntryStat(w io.Writer, entry drive.Entry) {
	kind := "file"
	if entry.IsDir {
		kind = "dir"
	}
	fmt.Fprintf(w, "type: %s\n", kind)
	fmt.Fprintf(w, "name: %s\n", entry.Name)
	fmt.Fprintf(w, "id: %s\n", entry.ID)
	fmt.Fprintf(w, "parent_id: %s\n", entry.ParentID)
	fmt.Fprintf(w, "size: %d\n", entry.Size)
	if !entry.ModTime.IsZero() {
		fmt.Fprintf(w, "mod_time: %s\n", entry.ModTime.Format(time.RFC3339))
	}
}

func CleanListPath(value string) string {
	if strings.TrimSpace(value) == "" {
		return "/"
	}
	return pathpkg.Clean("/" + strings.Trim(value, "/"))
}

func WritePrettyJSON(w io.Writer, value any) error {
	return util.WritePrettyJSON(w, value)
}

func FormatBytes(n int64) string {
	return util.FormatBytes(n)
}

func PendingFiles(fs any) ([]vfs.PendingUpload, error) {
	inspector, ok := fs.(vfs.UploadInspector)
	if !ok {
		return nil, fmt.Errorf("filesystem does not expose upload state")
	}
	return inspector.PendingUploads(), nil
}

func StagingStatus(item vfs.PendingUpload) (string, int64) {
	fi, err := os.Stat(item.LocalPath)
	if err != nil {
		return "missing", 0
	}
	if fi.Size() != item.Size {
		return "size-mismatch", fi.Size()
	}
	return "ok", fi.Size()
}

func FormatStagingStatus(status string, size int64) string {
	switch status {
	case "ok":
		return "ok"
	case "missing":
		return "missing"
	case "size-mismatch":
		return fmt.Sprintf("size-mismatch(%d)", size)
	default:
		return status
	}
}

func FormatUnixNano(ns int64) string {
	return util.FormatUnixNano(ns)
}
