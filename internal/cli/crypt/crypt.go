package crypt

import (
	"github.com/spf13/cobra"

	"github.com/yinzhenyu/qrypt/internal/cli/fs"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
)

// NewCommand builds the top-level crypt group. Filename transforms used to
// live under `qrypt fs crypt-encode|crypt-decode`; the fs paths remain as
// hidden, deprecated aliases for one minor release.
func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crypt",
		Short: "Transform filenames with a mount's encryption settings",
		Long: `Transform filenames between their plaintext virtual form and the
ciphertext form stored on the backend, using a mount's [mounts.encryption]
settings. These commands never touch remote data.`,
		Example: `  qrypt crypt encode /cloud/photos/cat.jpg
  qrypt crypt decode cloud /cloud/photos/8f3a2b...`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	rt.WithPersistentConfigFlag(cmd)
	cmd.AddCommand(newTransformCmd(rt, "encode", false))
	cmd.AddCommand(newTransformCmd(rt, "decode", true))
	return cmd
}

func newTransformCmd(rt cliruntime.Runtime, use string, decrypt bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use + " [MOUNT] NAME",
		Short: "Transform a filename with a mount's encryption config",
		Long: `Transform NAME with the mount's [mounts.encryption] settings. When
encoding, NAME is a plaintext virtual path; when decoding, NAME is the
ciphertext name as stored on the backend.

MOUNT defaults to the first mount with encryption configured.`,
		Example:           `  qrypt crypt ` + use + ` cloud /cloud/photos/cat.jpg`,
		Args:              cliruntime.RangeArgs(rt, 1, 2),
		RunE:              func(cmd *cobra.Command, args []string) error { return fs.RunCrypt(rt, cmd, args, decrypt) },
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}
