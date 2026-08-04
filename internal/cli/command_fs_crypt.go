package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
)

// fsCryptResult is the machine-readable outcome of crypt-encode/decode.
type fsCryptResult struct {
	Mount  string `json:"mount"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

func newFsCryptEncodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crypt-encode [MOUNT] NAME",
		Short: "Encrypt a filename with a mount's encryption config",
		Long: `Encrypt NAME (plaintext virtual path) into the ciphertext filename
stored on the backend, using the mount's [mounts.encryption] settings.
Useful to verify what a plaintext name looks like on the remote drive.

MOUNT defaults to the first mount with encryption configured.`,
		Args:              rangeArgs(1, 2),
		RunE:              func(cmd *cobra.Command, args []string) error { return runFsCrypt(cmd, args, false) },
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func newFsCryptDecodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crypt-decode [MOUNT] NAME",
		Short: "Decrypt an encrypted filename to its plaintext",
		Long: `Decrypt NAME (ciphertext name as stored on the backend) back to the
plaintext virtual path, using the mount's [mounts.encryption] settings.
Useful to identify which local file a backend filename belongs to.

MOUNT defaults to the first mount with encryption configured.`,
		Args:              rangeArgs(1, 2),
		RunE:              func(cmd *cobra.Command, args []string) error { return runFsCrypt(cmd, args, true) },
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runFsCrypt(cmd *cobra.Command, args []string, decrypt bool) error {
	state, err := commandConfig(cmd)
	if err != nil {
		return err
	}
	if state.cfg == nil {
		return configNotFoundError()
	}
	mountName := ""
	name := args[0]
	if len(args) == 2 {
		mountName, name = args[0], args[1]
	}
	selected, enc, err := selectEncryptedMount(state.cfg, mountName)
	if err != nil {
		return err
	}
	cp, err := crypt.NewRcloneCipherFromConfig(enc)
	if err != nil {
		return fmt.Errorf("mount %q: %w", selected, err)
	}
	output, err := transformCryptPath(cp, name, decrypt)
	if err != nil {
		return fmt.Errorf("mount %q: %w", selected, err)
	}
	result := fsCryptResult{Mount: selected, Input: name, Output: output}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		return writePrettyJSON(cmd.OutOrStdout(), result)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s -> %s\n", name, output)
	return nil
}

// selectEncryptedMount picks the mount whose encryption config will be used.
// An explicit name must match an encrypted mount (no fallback, so a typo is
// reported instead of silently using another mount's password).
func selectEncryptedMount(cfg *config.Config, name string) (string, crypt.Config, error) {
	if name != "" {
		for _, mount := range cfg.Mounts {
			if mount.Name != name {
				continue
			}
			enc := cfg.EncryptionFor(mount.Name)
			if enc.Password == "" {
				return "", crypt.Config{}, fmt.Errorf("mount %q has no encryption configured", name)
			}
			return mount.Name, enc, nil
		}
		return "", crypt.Config{}, fmt.Errorf("mount %q not found", name)
	}
	for _, mount := range cfg.Mounts {
		enc := cfg.EncryptionFor(mount.Name)
		if enc.Password != "" {
			return mount.Name, enc, nil
		}
	}
	return "", crypt.Config{}, fmt.Errorf("no encrypted mount found in config")
}

// transformCryptPath applies the filename cipher to each path segment, the
// same per-segment semantics the VFS uses for backend names.
func transformCryptPath(cp *crypt.RcloneCipher, path string, decrypt bool) (string, error) {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if segment == "" {
			continue
		}
		if decrypt {
			plain, err := cp.DecryptSegment(segment)
			if err != nil {
				return "", fmt.Errorf("segment %q: %w", segment, err)
			}
			segments[i] = plain
		} else {
			segments[i] = cp.EncryptSegment(segment)
		}
	}
	return strings.Join(segments, "/"), nil
}
