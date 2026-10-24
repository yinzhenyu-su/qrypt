package credential

import (
	"fmt"
	"io"
	"os"

	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
)

// Terminal detection seams. Tests override them to exercise both branches.
var (
	stdinIsTerminal = func(cmd *cobra.Command) bool {
		file, ok := cmd.InOrStdin().(*os.File)
		if !ok {
			return false
		}
		info, err := file.Stat()
		return err == nil && info.Mode()&os.ModeCharDevice != 0
	}
	stdoutIsTerminal = func(cmd *cobra.Command) bool {
		file, ok := cmd.OutOrStdout().(*os.File)
		if !ok {
			return false
		}
		info, err := file.Stat()
		return err == nil && info.Mode()&os.ModeCharDevice != 0
	}
)

type terminalLoginUI struct {
	out      io.Writer
	loc      *i18n.Localizer
	renderQR bool
}

func (u terminalLoginUI) ShowChallenge(challenge string) error {
	if u.renderQR {
		if qr, err := qrcode.New(challenge, qrcode.Medium); err == nil {
			fmt.Fprint(u.out, qr.ToSmallString(false))
		}
	}
	fmt.Fprintf(u.out, u.loc.T("out.credential.login.challenge")+"\n", challenge)
	return nil
}
