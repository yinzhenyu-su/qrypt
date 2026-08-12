package fs

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
)

// CryptResult is the machine-readable outcome of crypt-encode/decode.
type CryptResult struct {
	Mount  string `json:"mount"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

func NewCryptEncodeCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crypt-encode [MOUNT] NAME",
		Short: "Encrypt a filename with a mount's encryption config",
		Long: `Encrypt NAME (plaintext virtual path) into the ciphertext filename
stored on the backend, using the mount's [mounts.encryption] settings.
Useful to verify what a plaintext name looks like on the remote drive.

MOUNT defaults to the first mount with encryption configured.`,
		Args:              cliruntime.RangeArgs(rt, 1, 2),
		RunE:              func(cmd *cobra.Command, args []string) error { return RunCrypt(rt, cmd, args, false) },
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func NewCryptDecodeCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crypt-decode [MOUNT] NAME",
		Short: "Decrypt an encrypted filename to its plaintext",
		Long: `Decrypt NAME (ciphertext name as stored on the backend) back to the
plaintext virtual path, using the mount's [mounts.encryption] settings.
Useful to identify which local file a backend filename belongs to.

MOUNT defaults to the first mount with encryption configured.`,
		Args:              cliruntime.RangeArgs(rt, 1, 2),
		RunE:              func(cmd *cobra.Command, args []string) error { return RunCrypt(rt, cmd, args, true) },
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func RunCrypt(rt cliruntime.Runtime, cmd *cobra.Command, args []string, decrypt bool) error {
	state, err := rt.CommandConfig(cmd)
	if err != nil {
		return err
	}
	if state.Cfg == nil {
		return rt.ConfigNotFoundError()
	}
	mountName := ""
	name := args[0]
	if len(args) == 2 {
		mountName, name = args[0], args[1]
	}
	selected, enc, err := SelectEncryptedMount(state.Cfg, mountName)
	if err != nil {
		return err
	}
	cp, err := crypt.NewRcloneCipherFromConfig(enc)
	if err != nil {
		return fmt.Errorf("mount %q: %w", selected, err)
	}
	output, err := TransformCryptPath(cp, name, decrypt)
	if err != nil {
		return fmt.Errorf("mount %q: %w", selected, err)
	}
	result := CryptResult{Mount: selected, Input: name, Output: output}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), result)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s -> %s\n", name, output)
	return nil
}

// SelectEncryptedMount picks the mount whose encryption config will be used.
// An explicit name must match an encrypted mount (no fallback, so a typo is
// reported instead of silently using another mount's password).
func SelectEncryptedMount(cfg *config.Config, name string) (string, crypt.Config, error) {
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

// TransformCryptPath applies the filename cipher to each path segment, the
// same per-segment semantics the VFS uses for backend names.
func TransformCryptPath(cp *crypt.RcloneCipher, path string, decrypt bool) (string, error) {
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
