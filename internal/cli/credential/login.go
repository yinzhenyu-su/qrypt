package credential

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/cliutil"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Replaceable save seams keep bootstrap and update flows testable: tests can
// count writes and inject failures without depending on filesystem permissions.
var (
	saveExistingConfig = func(ctx context.Context, state cliruntime.ConfigState, mutate func(*config.Config) error) error {
		return cliutil.SaveConfigContext(ctx, state, mutate)
	}
	saveNewConfig = func(ctx context.Context, path string, cfg *config.Config) error {
		return cliutil.SaveNewConfigContext(ctx, path, cfg)
	}
)

func newLoginCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to a provider and save a credential",
		Long: `Run an interactive provider login, such as a QR-code scan, and save the
result as a shared credential. On an interactive terminal the command offers to
create a mount; pass --mount-name, --root-path, or --mount-point to create one
without prompting, or --no-input to save only the credential.`,
		Example: `  qrypt credential login --type quark
  qrypt credential login --type quark --name quark-main
  qrypt credential login --type quark --no-input`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogin(rt, cmd)
		},
	}
	cmd.Flags().String("type", "", "driver type to log in to")
	cmd.Flags().String("name", "", "credential name (reuse the same-account credential when omitted)")
	cmd.Flags().Duration("timeout", 5*time.Minute, "maximum wait for the login to complete")
	cmd.Flags().Bool("no-input", false, "skip the interactive mount wizard")
	cmd.Flags().String("mount-name", "", "mount name to create after login")
	cmd.Flags().String("root-path", "", "provider root path for the mount")
	cmd.Flags().String("mount-point", "", "local mount point for the namespace")
	return cmd
}

func runLogin(rt cliruntime.Runtime, cmd *cobra.Command) error {
	loc := i18n.FromCommand(cmd)
	providerType, _ := cmd.Flags().GetString("type")
	providerType = strings.TrimSpace(providerType)
	if providerType == "" {
		return rt.UsageError(cmd, "missing --type\n\nAvailable login providers: %s\n\nqrypt credential login --type <driver>", loginProviderList())
	}
	provider, ok := drive.LoginProviderFor(providerType)
	if !ok {
		return rt.UsageError(cmd, "driver %q does not support login\n\nAvailable login providers: %s\n\nqrypt credential login --type <driver>", providerType, loginProviderList())
	}

	timeout, _ := cmd.Flags().GetDuration("timeout")
	if timeout <= 0 {
		return rt.UsageError(cmd, "--timeout must be greater than zero\n\nqrypt credential login --type %s", providerType)
	}

	state, err := rt.CommandConfig(cmd)
	if err != nil {
		return err
	}
	if state.Cfg == nil && strings.TrimSpace(state.Path) == "" {
		state.Path = defaultLoginConfigPath()
	}

	noInput, _ := cmd.Flags().GetBool("no-input")
	interactive := stdinIsTerminal(cmd) && !noInput
	mountFlags := loginMountFlags(cmd)
	if state.Cfg == nil && !interactive && !mountFlags {
		return rt.UsageError(cmd,
			"no configuration file found; pass --mount-name and --mount-point, or run interactively\n\nqrypt credential login --type %s --mount-name quark --mount-point ~/Qrypt",
			providerType)
	}

	baseCtx, cancel := rt.ShutdownContext(cmd)
	defer cancel()
	loginCtx, cancelLogin := context.WithTimeout(baseCtx, timeout)

	ui := terminalLoginUI{
		out:      cmd.OutOrStdout(),
		loc:      loc,
		renderQR: stdoutIsTerminal(cmd),
	}
	fmt.Fprintln(cmd.ErrOrStderr(), loc.T("out.credential.login.waiting", timeout))
	result, err := provider.Login(loginCtx, ui)
	loginCtxErr := loginCtx.Err()
	cancelLogin()
	if loginCtxErr != nil {
		return loginErrorWithRetry(providerType, cmd, loginContextError(loginCtxErr))
	}
	if err != nil {
		return loginErrorWithRetry(providerType, cmd, err)
	}
	if err := baseCtx.Err(); err != nil {
		return loginErrorWithRetry(providerType, cmd, loginContextError(err))
	}
	account := result.Account
	if derived, known := provider.Account(result.Params); known {
		if account != "" && account != derived {
			return loginErrorWithRetry(providerType, cmd,
				fmt.Errorf("login provider %q reported account %q but its credential resolves to account %q; refusing to save", providerType, account, derived))
		}
		account = derived
	}

	layout := core.NewStorageLayout(state.Cfg, core.RuntimeLayout{})
	target, err := resolveLoginCredential(state.Cfg, layout, provider, providerType, account, cmd)
	if err != nil {
		return err
	}

	mountConfig, mountPoint, createMount, err := loginMountPlan(baseCtx, cmd, state, providerType, target.name, interactive, mountFlags)
	if err != nil {
		return loginErrorWithRetry(providerType, cmd, err)
	}

	params := config.ParamMap{}
	for key, value := range result.Params {
		params[key] = value
	}
	credential := config.CredentialConfig{Name: target.name, Type: providerType, Params: params}
	if err := baseCtx.Err(); err != nil {
		return loginErrorWithRetry(providerType, cmd, loginContextError(err))
	}

	if state.Cfg == nil {
		candidate := &config.Config{Version: "1"}
		candidate.Mount.MountPoint = mountPoint
		candidate.Credentials = []config.CredentialConfig{credential}
		candidate.Mounts = []config.MountConfig{mountConfig}
		if err := baseCtx.Err(); err != nil {
			return loginErrorWithRetry(providerType, cmd, loginContextError(err))
		}
		if err := saveNewConfig(baseCtx, state.Path, candidate); err != nil {
			return loginErrorWithRetry(providerType, cmd, err)
		}
	} else {
		if target.overrideUnknown {
			fmt.Fprintln(cmd.ErrOrStderr(), loc.T("out.credential.login.override_unknown", target.name))
		}
		if err := saveExistingConfig(baseCtx, state, func(candidate *config.Config) error {
			if err := baseCtx.Err(); err != nil {
				return loginContextError(err)
			}
			if err := candidate.UpsertCredential(credential); err != nil {
				return err
			}
			if !createMount {
				return nil
			}
			if _, exists := candidate.MountByName(mountConfig.Name); exists {
				return fmt.Errorf("mount %q already exists; choose another --mount-name", mountConfig.Name)
			}
			if err := candidate.UpsertMount(mountConfig); err != nil {
				return err
			}
			candidate.Mount.MountPoint = mountPoint
			if candidate.MountPoint != "" {
				candidate.MountPoint = mountPoint
			}
			return nil
		}); err != nil {
			return loginErrorWithRetry(providerType, cmd, err)
		}
	}

	accountLabel := account
	if accountLabel == "" {
		accountLabel = "-"
	}
	out := cmd.OutOrStdout()
	if createMount {
		fmt.Fprintf(out, "%s\n", loc.T("out.credential.login.saved_mount", target.name, accountLabel, mountConfig.Name, state.Path))
		fmt.Fprintln(out, loc.T("out.credential.login.next_mount"))
		return nil
	}
	fmt.Fprintf(out, "%s\n", loc.T("out.credential.login.saved", target.name, accountLabel, state.Path))
	fmt.Fprintln(out, loc.T("out.credential.login.next_mount_add", deriveMountName(target.name, state.Cfg), target.name, "/"))
	return nil
}

// loginErrorWithRetry appends a runnable retry command unless the error already
// carries one, so every provider, verification, wizard, and write failure ends
// with a concrete next step.
func loginErrorWithRetry(providerType string, cmd *cobra.Command, err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "qrypt credential login") {
		return redactLoginError(err)
	}
	return redactLoginError(fmt.Errorf("%w\n\nRetry: %s", err, loginRetryCommand(providerType, cmd)))
}

var (
	loginChallengeURLPattern = regexp.MustCompile(`(?i)https?://[^\s]*(?:token|st|service_ticket)=[^\s]*`)
	loginSecretQueryPattern  = regexp.MustCompile(`(?i)([?&](?:token|st|service_ticket)=)[^&\s]+`)
)

type redactedLoginError struct {
	err error
}

func (e redactedLoginError) Error() string {
	text := loginChallengeURLPattern.ReplaceAllString(e.err.Error(), "<redacted-challenge-url>")
	return loginSecretQueryPattern.ReplaceAllString(text, "${1}<redacted>")
}

func (e redactedLoginError) Unwrap() error { return e.err }

func redactLoginError(err error) error {
	if err == nil {
		return nil
	}
	return redactedLoginError{err: err}
}

func loginRetryCommand(providerType string, cmd *cobra.Command) string {
	parts := []string{"qrypt", "credential", "login", "--type", quoteLoginArg(providerType)}
	if flag := cmd.Flag("config"); flag != nil && flag.Changed {
		parts = append(parts, "--config", quoteLoginArg(flag.Value.String()))
	}
	if name, _ := cmd.Flags().GetString("name"); strings.TrimSpace(name) != "" {
		parts = append(parts, "--name", quoteLoginArg(strings.TrimSpace(name)))
	}
	if noInput, _ := cmd.Flags().GetBool("no-input"); noInput {
		parts = append(parts, "--no-input")
	}
	if flag := cmd.Flag("timeout"); flag != nil && flag.Changed {
		parts = append(parts, "--timeout", quoteLoginArg(flag.Value.String()))
	}
	for _, name := range []string{"mount-name", "root-path", "mount-point"} {
		if !cmd.Flags().Changed(name) {
			continue
		}
		value, _ := cmd.Flags().GetString(name)
		parts = append(parts, "--"+name, quoteLoginArg(value))
	}
	return strings.Join(parts, " ")
}

func quoteLoginArg(value string) string {
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t\n\"'\\$`") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func loginContextError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return errors.New("login cancelled; no configuration was written")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("login timed out; no configuration was written")
	default:
		return err
	}
}

func loginProviderList() string {
	names := drive.LoginProviderNames()
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

func defaultLoginConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".qrypt", "qrypt.toml")
	}
	return filepath.Join(home, ".qrypt", "qrypt.toml")
}
