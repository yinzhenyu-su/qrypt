package credential

import (
	"context"
	"fmt"
	"sort"
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

// NewCommand manages shared credentials: named account definitions that any
// number of mounts can reference through their `credential` field.
func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credential",
		Short: "Manage shared credentials",
		Long: `Create and inspect named credentials shared by one or more mounts.

A credential holds the account-scope parameters for a driver (a cookie, a
refresh token, a username and password). Mounts reference it by name and keep
only path-scope parameters such as root_path, so the same account can serve
several directories without copying the secret.`,
		Example: `  qrypt credential add main --type quark --param cookie="..."
  qrypt credential list
  qrypt credential show main`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	rt.WithPersistentConfigFlag(cmd)
	cmd.AddCommand(newAddCmd(rt))
	cmd.AddCommand(newLoginCmd(rt))
	cmd.AddCommand(newListCmd(rt))
	cmd.AddCommand(newShowCmd(rt))
	cmd.AddCommand(newSetCmd(rt))
	cmd.AddCommand(newRemoveCmd(rt))
	cmd.AddCommand(newMigrateCmd(rt))
	cmd.AddCommand(newAdoptCmd(rt))
	cmd.AddCommand(newTestCmd(rt))
	return cmd
}

func newAddCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add a shared credential",
		Long: `Add a named credential. --type selects the driver and --param supplies
credential-scope parameters such as cookie or refresh_token.`,
		Example: `  qrypt credential add main --type quark --param cookie="k1=v1; k2=v2"`,
		Args:    cliruntime.RangeArgs(rt, 1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			name := args[0]
			if _, exists := state.Cfg.CredentialFor(name); exists {
				return fmt.Errorf("credential %q already exists", name)
			}
			driverType, _ := cmd.Flags().GetString("type")
			params, err := cliutil.ParseParams(cmd, "param")
			if err != nil {
				return err
			}
			if err := cliutil.SaveConfig(state, func(candidate *config.Config) error {
				return candidate.UpsertCredential(config.CredentialConfig{
					Name:   name,
					Type:   driverType,
					Params: params,
				})
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", i18n.FromCommand(cmd).T("out.credential.added", name))
			return nil
		},
	}
	cmd.Flags().StringP("type", "t", "", "driver type, for example quark or 115")
	cmd.Flags().StringArrayP("param", "p", nil, "credential parameter as key=value (repeatable)")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func newListCmd(rt cliruntime.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List shared credentials",
		Long:    "List credentials with their driver type and the mounts that use them. Secret values are never printed.",
		Example: `  qrypt credential list`,
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
			layout := core.NewStorageLayout(state.Cfg, core.RuntimeLayout{})
			fmt.Fprintln(out, "NAME\tTYPE\tACCOUNT\tSOURCE\tVERSION\tUPDATED\tUSED BY")
			accounts := map[string]string{}
			for _, cred := range state.Cfg.Credentials {
				account, source, version, updated := "-", "-", "-", "-"
				provider, _ := drive.LoginProviderFor(cred.Type)
				info := core.ResolveCredentialAccount(state.Cfg, layout, cred.Name, provider)
				if info.Account != "" {
					account = info.Account
					key := cred.Type + "\x00" + info.Account
					if other, seen := accounts[key]; seen {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: credentials %q and %q resolve to the same %s account; merge them to share one lifecycle\n", other, cred.Name, cred.Type)
					} else {
						accounts[key] = cred.Name
					}
				}
				if info.Source != "" {
					source = info.Source
				}
				if info.Source == core.CredentialAccountSourceSession {
					version = fmt.Sprintf("%d", info.Version)
					if !info.UpdatedAt.IsZero() {
						updated = info.UpdatedAt.Format(time.RFC3339)
					}
				}
				fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", cred.Name, cred.Type, account, source, version, updated, strings.Join(state.Cfg.CredentialUsedBy(cred.Name), ","))
			}
			return nil
		},
	}
}

func newShowCmd(rt cliruntime.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:     "show NAME",
		Short:   "Show one shared credential",
		Long:    "Show a credential's driver type, referencing mounts, and parameters. Secret values are masked.",
		Example: `  qrypt credential show main`,
		Args:    cliruntime.RangeArgs(rt, 1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			cred, ok := state.Cfg.CredentialFor(args[0])
			if !ok {
				return fmt.Errorf("credential %q not found", args[0])
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "name: %s\ntype: %s\nused_by: %s\n", cred.Name, cred.Type, strings.Join(state.Cfg.CredentialUsedBy(cred.Name), ","))
			layout := core.NewStorageLayout(state.Cfg, core.RuntimeLayout{})
			provider, _ := drive.LoginProviderFor(cred.Type)
			info := core.ResolveCredentialAccount(state.Cfg, layout, cred.Name, provider)
			account, source, version, updated := info.Account, info.Source, "-", "-"
			if account == "" {
				account = "-"
			}
			if source == "" {
				source = "-"
			}
			if info.Source == core.CredentialAccountSourceSession {
				version = fmt.Sprintf("%d", info.Version)
				if !info.UpdatedAt.IsZero() {
					updated = info.UpdatedAt.Format(time.RFC3339)
				}
			}
			fmt.Fprintf(out, "account: %s\nsource: %s\nversion: %s\nupdated_at: %s\n", account, source, version, updated)
			for _, param := range credentialParams(cred) {
				fmt.Fprintf(out, "%s = %s\n", param.name, param.value)
			}
			return nil
		},
	}
}

func newSetCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set NAME",
		Short: "Update a shared credential",
		Long: `Update a credential's driver type or parameters. --param sets or replaces a
value; --remove-param deletes one. Mounts that reference the credential pick
up the change without editing their own entry.`,
		Example: `  qrypt credential set main --param cookie="k1=v2"
  qrypt credential set main --remove-param wg_keys`,
		Args: cliruntime.RangeArgs(rt, 1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			cred, ok := state.Cfg.CredentialFor(args[0])
			if !ok {
				return fmt.Errorf("credential %q not found", args[0])
			}
			if cmd.Flags().Changed("type") {
				cred.Type, _ = cmd.Flags().GetString("type")
			}
			params, err := cliutil.ParseParams(cmd, "param")
			if err != nil {
				return err
			}
			if cred.Params == nil {
				cred.Params = config.ParamMap{}
			}
			for key, value := range params {
				cred.Params[key] = value
			}
			removed, _ := cmd.Flags().GetStringArray("remove-param")
			for _, key := range removed {
				delete(cred.Params, strings.TrimSpace(key))
			}
			if err := cliutil.SaveConfig(state, func(candidate *config.Config) error {
				return candidate.UpsertCredential(cred)
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", i18n.FromCommand(cmd).T("out.credential.updated", cred.Name))
			return nil
		},
	}
	cmd.Flags().StringP("type", "t", "", "change the driver type")
	cmd.Flags().StringArrayP("param", "p", nil, "credential parameter as key=value (repeatable)")
	cmd.Flags().StringArray("remove-param", nil, "credential parameter name to delete (repeatable)")
	return cmd
}

func newRemoveCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a shared credential",
		Long:  "Remove a credential that no mount references. Move or remove its mounts first. --purge also deletes the persisted session directory.",
		Example: `  qrypt credential remove main
  qrypt credential remove main --purge`,
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
				return candidate.RemoveCredential(args[0])
			}); err != nil {
				return err
			}
			if purge, _ := cmd.Flags().GetBool("purge"); purge {
				layout := core.NewStorageLayout(state.Cfg, core.RuntimeLayout{})
				if err := core.PurgeCredentialState(layout, args[0]); err != nil {
					return err
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", i18n.FromCommand(cmd).T("out.credential.removed", args[0]))
			return nil
		},
	}
	cmd.Flags().Bool("purge", false, "also delete the persisted credential session")
	return cmd
}

func newAdoptCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adopt MOUNT",
		Short: "Convert an inline mount credential into a shared credential",
		Long: `Move a mount's inline credential-scope parameters into a new credential and
point the mount at it. The mount's legacy rotation state is adopted when it
belongs to the same account; the legacy file is kept unless --delete-legacy is
given.`,
		Example: `  qrypt credential adopt docs --name main
  qrypt credential adopt docs --name main --delete-legacy`,
		Args: cliruntime.RangeArgs(rt, 1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			name, _ := cmd.Flags().GetString("name")
			deleteLegacy, _ := cmd.Flags().GetBool("delete-legacy")
			layout := core.NewStorageLayout(state.Cfg, core.RuntimeLayout{})
			adoption, err := core.AdoptMountCredential(state.Cfg, layout, args[0], name, deleteLegacy)
			if err != nil {
				return err
			}
			if err := config.Validate(state.Cfg); err != nil {
				return err
			}
			if err := config.Save(state.Path, state.Cfg); err != nil {
				return err
			}
			if adoption.StateAdopted {
				fmt.Fprintln(cmd.OutOrStdout(), i18n.FromCommand(cmd).T("out.credential.adopted", args[0], adoption.Credential))
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), i18n.FromCommand(cmd).T("out.credential.adopted_without_state", args[0], adoption.Credential, adoption.Reason))
			}
			return nil
		},
	}
	cmd.Flags().String("name", "", "new credential name (required)")
	cmd.Flags().Bool("delete-legacy", false, "delete the legacy per-mount state after adoption")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newTestCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test NAME",
		Short: "Authenticate a credential without mounting",
		Long: `Build the driver from a credential and run its Init once, then drop it.
Mount-scope requirements of the driver (bucket, address, root_path, ...) can
be supplied with --param. The check may refresh and rotate the shared session.`,
		Example: `  qrypt credential test main
  qrypt credential test main --param root_path=/
  qrypt credential test s3-main --param bucket=my-bucket`,
		Args: cliruntime.RangeArgs(rt, 1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			params, err := cliutil.ParseParams(cmd, "param")
			if err != nil {
				return err
			}
			timeout, _ := cmd.Flags().GetDuration("timeout")
			layout := core.NewStorageLayout(state.Cfg, core.RuntimeLayout{})
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			if err := core.TestCredential(ctx, state.Cfg, layout, args[0], params, timeout); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), i18n.FromCommand(cmd).T("out.credential.test.ok", args[0]))
			return nil
		},
	}
	cmd.Flags().StringArrayP("param", "p", nil, "mount-scope parameter as key=value (repeatable)")
	cmd.Flags().Duration("timeout", 30*time.Second, "authentication timeout")
	return cmd
}

func newMigrateCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate MOUNT",
		Short: "Move a legacy session into a credential",
		Long: `Adopt the rotation state a mount accumulated before it referenced a shared
credential. The driver only adopts state that belongs to the credential's
account; otherwise nothing is migrated. The legacy file is kept unless
--delete-legacy is given.`,
		Example: `  qrypt credential migrate docs --credential main
  qrypt credential migrate docs --credential main --delete-legacy`,
		Args: cliruntime.RangeArgs(rt, 1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := rt.CommandConfig(cmd)
			if err != nil {
				return err
			}
			if state.Cfg == nil {
				return rt.ConfigNotFoundError()
			}
			credentialName, _ := cmd.Flags().GetString("credential")
			deleteLegacy, _ := cmd.Flags().GetBool("delete-legacy")
			layout := core.NewStorageLayout(state.Cfg, core.RuntimeLayout{})
			result, err := core.MigrateCredentialSession(state.Cfg, layout, args[0], credentialName, deleteLegacy)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !result.Adopted {
				fmt.Fprintln(out, i18n.FromCommand(cmd).T("out.credential.migrate.skipped", args[0], result.Reason))
				return nil
			}
			fmt.Fprintln(out, i18n.FromCommand(cmd).T("out.credential.migrate.adopted", args[0], result.Account))
			return nil
		},
	}
	cmd.Flags().String("credential", "", "target credential name")
	cmd.Flags().Bool("delete-legacy", false, "delete the legacy state file after adoption")
	_ = cmd.MarkFlagRequired("credential")
	return cmd
}

type credentialParam struct {
	name  string
	value string
}

func credentialParams(cred config.CredentialConfig) []credentialParam {
	secret := map[string]bool{}
	for _, param := range drive.ParamSchema(cred.Type) {
		secret[param.Name] = param.Secret
	}
	keys := make([]string, 0, len(cred.Params))
	for key := range cred.Params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	params := make([]credentialParam, 0, len(keys))
	for _, key := range keys {
		value := cred.Params[key]
		if secret[key] {
			value = "***"
		}
		params = append(params, credentialParam{name: key, value: value})
	}
	return params
}
