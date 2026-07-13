package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

func newConfigExportRclonePasswordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export-rclone-password [MOUNT_NAME]",
		Short: "Print the rclone-compatible password for a mount",
		Long: `Print the password rclone needs to decrypt files encrypted by this config.

If password_hash is "argon2id", the Argon2id-derived key is printed as a hex string.
Otherwise, the raw password is printed unchanged.

Use with a config file (reads encryption settings from the named mount):
  rclone config update myremote password=$(qrypt config export-rclone-password mymount)

Or compute directly from raw inputs (no config file needed):
  qrypt config export-rclone-password --password-file ./password.txt --salt "mysalt"`,
		Args: maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rawPassword, direct, err := directPasswordFromFlags(cmd)
			if err != nil {
				return err
			}
			rawSalt, _ := cmd.Flags().GetString("salt")
			passwordHash, _ := cmd.Flags().GetString("password-hash")

			if direct {
				if len(args) != 0 {
					return fmt.Errorf("MOUNT_NAME cannot be used with a direct password source")
				}
				if cmd.Flags().Changed("config") {
					return fmt.Errorf("--config cannot be used with a direct password source")
				}
				switch passwordHash {
				case "argon2id":
				case "none":
					passwordHash = ""
				default:
					return fmt.Errorf("--password-hash must be argon2id or none")
				}
				pw, err := exportDirect(rawPassword, rawSalt, passwordHash)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), pw)
				return nil
			}

			state, err := commandConfig(cmd)
			if err != nil {
				return err
			}
			if state.cfg == nil {
				return fmt.Errorf("%w; alternatively use --password-file or --password-stdin", configNotFoundError())
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Config: %s\n", state.path)
			if len(args) == 0 && len(state.cfg.Mounts) > 1 {
				return fmt.Errorf("MOUNT_NAME is required when the config contains multiple mounts")
			}
			mountName := ""
			if len(args) == 1 {
				mountName = args[0]
				found := false
				for _, mount := range state.cfg.Mounts {
					if mount.Name == mountName {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("mount %q not found in config", mountName)
				}
			}
			enc := state.cfg.EncryptionFor(mountName)
			pw, err := crypt.ExportRclonePassword(enc)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), pw)
			return nil
		},
	}
	withConfigFlag(cmd)
	cmd.Flags().String("password", "", "raw password (visible in shell history; overrides config)")
	cmd.Flags().String("password-file", "", "read raw password from a file")
	cmd.Flags().Bool("password-stdin", false, "read raw password from standard input")
	cmd.Flags().String("salt", "", "salt for key derivation (used with a direct password source)")
	cmd.Flags().String("password-hash", crypt.PasswordHashArgon2id, "password hash mode: argon2id or none")
	return cmd
}

func directPasswordFromFlags(cmd *cobra.Command) (string, bool, error) {
	password, _ := cmd.Flags().GetString("password")
	passwordFile, _ := cmd.Flags().GetString("password-file")
	passwordStdin, _ := cmd.Flags().GetBool("password-stdin")

	sources := 0
	for _, name := range []string{"password", "password-file", "password-stdin"} {
		if cmd.Flags().Changed(name) {
			sources++
		}
	}
	if sources > 1 {
		return "", false, fmt.Errorf("use only one of --password, --password-file, or --password-stdin")
	}
	if sources == 0 {
		if cmd.Flags().Changed("salt") || cmd.Flags().Changed("password-hash") {
			return "", false, fmt.Errorf("--salt and --password-hash require a direct password source")
		}
		return "", false, nil
	}
	if passwordFile != "" {
		raw, err := os.ReadFile(osutil.ExpandHome(passwordFile))
		if err != nil {
			return "", false, fmt.Errorf("read password file: %w", err)
		}
		password = trimPasswordLine(raw)
	}
	if passwordStdin {
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", false, fmt.Errorf("read password from stdin: %w", err)
		}
		password = trimPasswordLine(raw)
	}
	return password, true, nil
}

func trimPasswordLine(raw []byte) string {
	return strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
}

func exportDirect(password, salt, passwordHash string) (string, error) {
	cfg := crypt.Config{
		Password:     password,
		Salt:         salt,
		PasswordHash: passwordHash,
	}
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	pw, err := crypt.ExportRclonePassword(cfg)
	if err != nil {
		return "", err
	}
	return pw, nil
}
