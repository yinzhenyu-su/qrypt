package cli

import (
	"github.com/spf13/cobra"
	clifs "github.com/yinzhenyu/qrypt/internal/cli/fs"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
)

type fsCryptResult = clifs.CryptResult

func runFsCrypt(cmd *cobra.Command, args []string, decrypt bool) error {
	return clifs.RunCrypt(cliRuntime{}, cmd, args, decrypt)
}

func selectEncryptedMount(cfg *config.Config, name string) (string, crypt.Config, error) {
	return clifs.SelectEncryptedMount(cfg, name)
}

func transformCryptPath(cp *crypt.RcloneCipher, path string, decrypt bool) (string, error) {
	return clifs.TransformCryptPath(cp, path, decrypt)
}
