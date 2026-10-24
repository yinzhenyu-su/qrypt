package mount

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/cliutil"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/mount"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// reservedMountNames are subcommand names, so a mount cannot use them for the
// `qrypt mount NAME` shorthand.
var reservedMountNames = map[string]bool{
	"add":    true,
	"set":    true,
	"remove": true,
	"list":   true,
}

// ReservedMountName reports whether name is a mount subcommand and therefore
// cannot be used as a mount name in the `qrypt mount NAME` shorthand.
func ReservedMountName(name string) bool {
	return reservedMountNames[name]
}

func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount [MOUNT_NAME]",
		Short: "Mount configured drives with FUSE",
		Long:  "Mount all configured drives as one local FUSE filesystem, or mount one configured drive by name. Uses mount_point from config unless --mount-point is set.",
		Example: `  qrypt mount
  qrypt mount cloud --mount-point /mnt/qrypt`,
		Args: cliruntime.MaxArgs(rt, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			ctx, stop := rt.ShutdownContext(cmd)
			defer stop()

			debugListen := ""
			mountPointFlag, _ := cmd.Flags().GetString("mount-point")
			nameFlag, _ := cmd.Flags().GetString("name")
			selectedMount := ""
			switch {
			case nameFlag != "" && len(args) == 1:
				return fmt.Errorf("use either --name or a positional mount name, not both")
			case nameFlag != "":
				selectedMount = nameFlag
			case len(args) == 1:
				selectedMount = args[0]
			}

			coreHost, err := rt.OpenCoreForMount(ctx, state.Cfg, state.Path, selectedMount, debugListen, "")
			if err != nil {
				return err
			}
			defer func() {
				// Core owns the filesystem, task manager, debug server and
				// external driver cleanup. It is closed only after the unmount
				// defer has stopped new FUSE requests.
				if err := coreHost.Close(context.Background()); err != nil {
					logging.L.Warnf("[CLI] core close: %v", err)
				}
			}()
			fs := coreHost.FileSystem()

			mountPoint := ""
			if mountPointFlag != "" {
				mountPoint = mountPointFlag
			} else {
				var err error
				mountPoint, err = rt.MountPointFromConfig(state.Cfg)
				if err != nil {
					return err
				}
			}

			mountOpts, err := rt.MountOptionsFromConfig(state.Cfg)
			if err != nil {
				return err
			}

			mountPointExpanded := util.ExpandHome(mountPoint)
			loc := i18n.FromCommand(cmd)
			logging.L.Infof("Mounting at %s ...", mountPointExpanded)
			fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", loc.T("out.mount.mounting", mountPointExpanded))
			mountOpts.MountPoint = mountPointExpanded
			mountOpts.Foreground = true
			session, err := mount.NewMounter().Mount(ctx, fs, mountOpts)
			if err != nil {
				logging.L.Errorf("Mount failed: %v", err)
				return err
			}
			// Unmount exactly once on every exit path. The signal path below
			// calls unmount() explicitly; the deferred call covers panics and
			// unexpected returns, which would otherwise leave the mount point
			// behind (the FUSE kernel drops the connection when the process
			// dies, but the directory and in-flight writes are not cleaned).
			// sync.Once keeps the two paths from double-unmounting.
			var unmountOnce sync.Once
			unmount := func() {
				unmountOnce.Do(func() {
					logging.L.Infof("Unmounting %s ...", mountPointExpanded)
					fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", loc.T("out.mount.unmounting", mountPointExpanded))
					uctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := mount.NewMounter().Unmount(uctx, session); err != nil {
						logging.L.Warnf("Unmount failed: %v", err)
					}
				})
			}
			defer unmount()
			logging.L.Infof("Mounted at %s. Press Ctrl+C to unmount.", mountPointExpanded)
			fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", loc.T("out.mount.mounted", mountPointExpanded))
			if prefetcher, ok := fs.(interface{ StartDirectoryPrefetch(context.Context) }); ok {
				prefetcher.StartDirectoryPrefetch(ctx)
			}
			<-ctx.Done()
			unmount()
			return nil
		},
	}
	rt.WithRuntimeConfigFlag(cmd)
	cmd.Flags().String("mount-point", "", "local FUSE mount point (defaults to config mount_point)")
	cmd.Flags().String("name", "", "mount this configured drive (use when its name collides with a subcommand)")
	cmd.AddCommand(newAddCmd(rt))
	cmd.AddCommand(newSetCmd(rt))
	cmd.AddCommand(newRemoveCmd(rt))
	cmd.AddCommand(newListCmd(rt))
	return cmd
}

func newAddCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add a mount from an existing credential",
		Long: `Add a mount that uses a credential declared in [[credentials]]. The driver
type comes from the credential, so only mount-scope attributes (root_path and
other path-scope params, test_enabled, optional encryption) are configured
here. Encryption flags mirror [mounts.encryption]: --password enables it, and
--salt / --filename-encryption / --filename-encoding / --content-dedup tune it.
Mount names add, set, remove, and list are reserved for subcommands.`,
		Example: `  qrypt mount add docs --credential main --param root_path=/Docs
  qrypt mount add photos --credential main --param root_path=/Photos --password "secret"
  qrypt mount add vault --credential main --param root_path=/Vault --password "secret" --filename-encryption obfuscate`,
		Args: cliruntime.RangeArgs(rt, 1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			name := args[0]
			if reservedMountNames[name] {
				return fmt.Errorf("mount name %q is reserved for a subcommand", name)
			}
			if _, exists := state.Cfg.MountByName(name); exists {
				return fmt.Errorf("mount %q already exists", name)
			}
			credentialName, _ := cmd.Flags().GetString("credential")
			credential, ok := state.Cfg.CredentialFor(credentialName)
			if !ok {
				return fmt.Errorf("credential %q not found; create it with qrypt credential add", credentialName)
			}
			params, err := cliutil.ParseParams(cmd, "param")
			if err != nil {
				return err
			}
			testEnabled, _ := cmd.Flags().GetBool("test-enabled")
			password, passwordProvided, err := cliutil.PasswordFromFlags(cmd)
			if err != nil {
				return err
			}
			salt, _ := cmd.Flags().GetString("salt")
			filenameEncryption, _ := cmd.Flags().GetString("filename-encryption")
			filenameEncoding, _ := cmd.Flags().GetString("filename-encoding")
			contentDedup, _ := cmd.Flags().GetBool("content-dedup")
			if contentDedup && !passwordProvided {
				return fmt.Errorf("--content-dedup requires an encryption password")
			}
			if !passwordProvided && (salt != "" || filenameEncryption != "" || filenameEncoding != "") {
				return fmt.Errorf("--salt, --filename-encryption, and --filename-encoding require an encryption password")
			}
			mount := config.MountConfig{
				Name:        name,
				Type:        credential.Type,
				Credential:  credential.Name,
				TestEnabled: testEnabled,
				Params:      params,
			}
			if passwordProvided {
				mount.Encryption = &crypt.Config{
					Password:           password,
					Salt:               salt,
					FileNameEncryption: filenameEncryption,
					FileNameEncoding:   filenameEncoding,
					ContentDedup:       contentDedup,
				}
			}
			if err := cliutil.SaveConfig(state, func(candidate *config.Config) error {
				return candidate.UpsertMount(mount)
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", i18n.FromCommand(cmd).T("out.mount.added", name, credential.Name))
			return nil
		},
	}
	rt.WithConfigFlag(cmd)
	cmd.Flags().String("credential", "", "credential to use (required)")
	cmd.Flags().StringArrayP("param", "p", nil, "mount parameter as key=value (repeatable)")
	cmd.Flags().Bool("test-enabled", false, "allow probe and benchmark commands to use this mount")
	cmd.Flags().String("password", "", "optional encryption password for this mount")
	cmd.Flags().String("password-file", "", "read the encryption password from a file")
	cmd.Flags().Bool("password-stdin", false, "read the encryption password from standard input")
	cmd.Flags().String("salt", "", "encryption salt (requires an encryption password)")
	cmd.Flags().String("filename-encryption", "", "filename mode: standard, obfuscate, or off (requires --password)")
	cmd.Flags().String("filename-encoding", "", "filename encoding: base32 or base64 (requires --password)")
	cmd.Flags().Bool("content-dedup", false, "enable content de-duplication (requires --password)")
	_ = cmd.MarkFlagRequired("credential")
	return cmd
}

func newSetCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set NAME",
		Short: "Update a mount's credential or attributes",
		Long: `Update an existing mount. --credential switches it to another declared
credential (the driver type follows the new credential); --param sets or
replaces a mount-scope parameter and --remove-param deletes one.

Encryption flags mirror [mounts.encryption]. Adding encryption to an
unencrypted mount needs only --password. Encryption cannot be removed once
configured; changing its password, salt, or filename mode requires --force,
because existing remote data may become unreadable.`,
		Example: `  qrypt mount set docs --param root_path=/Documents
  qrypt mount set docs --credential other-main
  qrypt mount set docs --password "new-secret" --force`,
		Args: cliruntime.RangeArgs(rt, 1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			mount, ok := state.Cfg.MountByName(args[0])
			if !ok {
				return fmt.Errorf("mount %q not found", args[0])
			}
			if cmd.Flags().Changed("credential") {
				credentialName, _ := cmd.Flags().GetString("credential")
				credential, ok := state.Cfg.CredentialFor(credentialName)
				if !ok {
					return fmt.Errorf("credential %q not found; create it with qrypt credential add", credentialName)
				}
				mount.Credential = credential.Name
				mount.Type = credential.Type
			}
			params, err := cliutil.ParseParams(cmd, "param")
			if err != nil {
				return err
			}
			if mount.Params == nil {
				mount.Params = config.ParamMap{}
			}
			for key, value := range params {
				mount.Params[key] = value
			}
			removed, _ := cmd.Flags().GetStringArray("remove-param")
			for _, key := range removed {
				delete(mount.Params, strings.TrimSpace(key))
			}
			if cmd.Flags().Changed("test-enabled") {
				mount.TestEnabled, _ = cmd.Flags().GetBool("test-enabled")
			}
			nextEncryption, err := applyEncryptionFlags(cmd, mount.Encryption)
			if err != nil {
				return err
			}
			force, _ := cmd.Flags().GetBool("force")
			if err := guardEncryptionChange(mount.Encryption, nextEncryption, force); err != nil {
				return fmt.Errorf("mount %q: %w", mount.Name, err)
			}
			mount.Encryption = nextEncryption
			if err := cliutil.SaveConfig(state, func(candidate *config.Config) error {
				return candidate.UpsertMount(mount)
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", i18n.FromCommand(cmd).T("out.mount.updated", mount.Name))
			return nil
		},
	}
	rt.WithConfigFlag(cmd)
	cmd.Flags().String("credential", "", "switch to another declared credential")
	cmd.Flags().StringArrayP("param", "p", nil, "mount parameter as key=value (repeatable)")
	cmd.Flags().StringArray("remove-param", nil, "mount parameter name to delete (repeatable)")
	cmd.Flags().Bool("test-enabled", false, "allow probe and benchmark commands to use this mount")
	cmd.Flags().String("password", "", "encryption password (adds or replaces encryption)")
	cmd.Flags().String("password-file", "", "read the encryption password from a file")
	cmd.Flags().Bool("password-stdin", false, "read the encryption password from standard input")
	cmd.Flags().String("salt", "", "encryption salt")
	cmd.Flags().String("filename-encryption", "", "filename mode: standard, obfuscate, or off")
	cmd.Flags().String("filename-encoding", "", "filename encoding: base32 or base64")
	cmd.Flags().Bool("content-dedup", false, "enable content de-duplication")
	cmd.Flags().Bool("force", false, "confirm an encryption change that may make existing data unreadable")
	return cmd
}

// applyEncryptionFlags merges the encryption flags onto the current mount
// encryption. Existing encryption is never removed by this command.
func applyEncryptionFlags(cmd *cobra.Command, current *crypt.Config) (*crypt.Config, error) {
	password, passwordProvided, err := cliutil.PasswordFromFlags(cmd)
	if err != nil {
		return nil, err
	}
	changed := passwordProvided
	for _, name := range []string{"salt", "filename-encryption", "filename-encoding", "content-dedup"} {
		if cmd.Flags().Changed(name) {
			changed = true
		}
	}
	if !changed {
		return current, nil
	}
	next := crypt.Config{}
	if current != nil {
		next = *current
	}
	if passwordProvided {
		next.Password = password
	}
	if cmd.Flags().Changed("salt") {
		next.Salt, _ = cmd.Flags().GetString("salt")
	}
	if cmd.Flags().Changed("filename-encryption") {
		next.FileNameEncryption, _ = cmd.Flags().GetString("filename-encryption")
	}
	if cmd.Flags().Changed("filename-encoding") {
		next.FileNameEncoding, _ = cmd.Flags().GetString("filename-encoding")
	}
	if cmd.Flags().Changed("content-dedup") {
		next.ContentDedup, _ = cmd.Flags().GetBool("content-dedup")
	}
	if next.Password == "" {
		return nil, fmt.Errorf("--password is required to configure encryption")
	}
	return &next, nil
}

// guardEncryptionChange rejects turning an encrypted mount into plaintext and
// requires --force for any other change to an existing encryption block.
func guardEncryptionChange(current, next *crypt.Config, force bool) error {
	if current == nil {
		return nil
	}
	if next == nil {
		return fmt.Errorf("an encrypted mount cannot be switched back to plaintext")
	}
	if !reflect.DeepEqual(current, next) && !force {
		return fmt.Errorf("changing encryption requires --force; existing remote data may become unreadable")
	}
	return nil
}

func newRemoveCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a mount",
		Long:  "Remove a mount from the configuration. The referenced credential is kept; remove it separately with qrypt credential remove. --purge also deletes the mount's local state directory.",
		Example: `  qrypt mount remove docs
  qrypt mount remove docs --purge`,
		Args: cliruntime.RangeArgs(rt, 1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			if err := cliutil.SaveConfig(state, func(candidate *config.Config) error {
				return candidate.RemoveMount(args[0])
			}); err != nil {
				return err
			}
			if purge, _ := cmd.Flags().GetBool("purge"); purge {
				layout := core.NewStorageLayout(state.Cfg, core.RuntimeLayout{})
				if err := core.PurgeMountState(layout, args[0]); err != nil {
					return err
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", i18n.FromCommand(cmd).T("out.mount.removed", args[0]))
			return nil
		},
	}
	rt.WithConfigFlag(cmd)
	cmd.Flags().Bool("purge", false, "also delete the mount's local state directory")
	return cmd
}

func newListCmd(rt cliruntime.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List configured mounts",
		Long:    "List mounts with their driver type, referenced credential, and configured root_path.",
		Example: `  qrypt mount list`,
		Args:    cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "NAME\tTYPE\tCREDENTIAL\tROOT_PATH")
			for _, mount := range state.Cfg.Mounts {
				credential := mount.Credential
				if credential == "" {
					credential = "(inline)"
				}
				rootPath := mount.Params["root_path"]
				if rootPath == "" {
					rootPath = "/"
				}
				fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", mount.Name, mount.Type, credential, rootPath)
			}
			return nil
		},
	}
}
