// Package share implements the provider-neutral `qrypt share` command group.
// Every command talks to a running Core over the local control socket; none of
// them needs a provider name, driver name, socket path, or provider share id.
package share

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/controlctx"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
)

// passcodeGenerated is the sentinel cobra stores for `--passcode` without a
// value, before the CLI turns it into a local generation request.
const passcodeGenerated = "\x00generate"

// NewCommand builds the `share` command group.
func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "share",
		Short: "Publish and manage links to content already in a mounted drive",
		Long: `Publish provider share links for existing content and manage them
afterwards. The mount that owns the path selects the provider, so no command
needs a provider name or a provider share identifier.

  qrypt share create /quark/report.pdf --expires 7d --passcode
  qrypt share list
  qrypt share show <link-id>`,
		Example: `  qrypt share create /quark/report.pdf --expires 7d
  qrypt share create /quark/docs/a.bin /quark/docs/b.bin --passcode=ab12 --name release
  qrypt share list --mount quark
  qrypt share show abcd1234efgh --show-secrets
  qrypt share update abcd1234efgh --expires 30d --public
  qrypt share remove abcd1234efgh`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	rt.WithPersistentConfigFlag(cmd)
	cmd.AddCommand(newCreateCommand(rt))
	cmd.AddCommand(newListCommand(rt))
	cmd.AddCommand(newShowCommand(rt))
	cmd.AddCommand(newUpdateCommand(rt))
	cmd.AddCommand(newRemoveCommand(rt))
	return cmd
}

func createArgs(rt cliruntime.ErrorFactory) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return rt.UsageError(cmd, "missing PATH\n\nPATH is a mount-qualified path to existing content, for example:\n  qrypt share create /MOUNT/DIR/FILE --expires 7d")
		}
		return nil
	}
}

func linkIDArgs(rt cliruntime.ErrorFactory, command string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) == 0:
			return rt.UsageError(cmd, "missing LINK_ID\n\nRun 'qrypt share list' to see link ids, or 'qrypt share create PATH' to publish one.")
		case len(args) > 1:
			return rt.UsageError(cmd, "too many arguments: %s\n\nPass exactly one LINK_ID.", strings.Join(args[1:], " "))
		default:
			return nil
		}
	}
}

func removeArgs(rt cliruntime.ErrorFactory) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return rt.UsageError(cmd, "missing LINK_ID\n\nPass one or more link ids from 'qrypt share list', for example:\n  qrypt share remove a1b2c3d4e5f6")
		}
		return nil
	}
}

// parseShareExpiry maps the neutral expiry vocabulary onto a duration.
func parseShareExpiry(raw string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1d", "day":
		return drive.ShareExpiryDay, nil
	case "7d", "week":
		return drive.ShareExpiryWeek, nil
	case "30d", "month":
		return drive.ShareExpiryMonth, nil
	case "forever", "permanent", "never", "0":
		return drive.ShareExpiryPermanent, nil
	default:
		return 0, fmt.Errorf("unsupported --expires value %q (use 1d, 7d, 30d, or forever)", raw)
	}
}

// passcodeFlag adds a --passcode flag whose bare form asks for a local
// generation instead of exposing a value in shell history.
func passcodeFlag(cmd *cobra.Command) {
	cmd.Flags().String("passcode", "", "link passcode; omit the value to generate one")
	cmd.Flags().Lookup("passcode").NoOptDefVal = passcodeGenerated
}

// resolvePasscode reports the explicit passcode and whether the CLI must
// generate one. A bare `--passcode` returns an empty value plus generate=true.
func resolvePasscode(rt cliruntime.ErrorFactory, cmd *cobra.Command) (passcode string, generate bool, err error) {
	if !cmd.Flags().Changed("passcode") {
		return "", false, nil
	}
	raw, _ := cmd.Flags().GetString("passcode")
	if raw == passcodeGenerated {
		return "", true, nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true, nil
	}
	if len(raw) > 16 {
		return "", false, rt.UsageError(cmd, "--passcode must be at most 16 characters")
	}
	for _, r := range raw {
		if r < 0x21 || r > 0x7e {
			return "", false, rt.UsageError(cmd, "--passcode must be printable ASCII without spaces")
		}
	}
	return raw, false, nil
}

func newCreateCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create PATH...",
		Short: "Publish one link for existing content",
		Long: `Publish one link for existing content in a mounted drive. All paths must
belong to the same mount; the mount selects the provider. The link is returned
immediately and printed with its URL and expiry.

A provider that answers asynchronously is polled inside the driver under a
bounded window, so a queued link either finishes or reports share_timeout.`,
		Example: `  qrypt share create /quark/report.pdf --expires 7d
  qrypt share create /quark/docs/a.bin /quark/docs/b.bin --passcode --name release
  qrypt share create /quark/photos --expires forever --limit-visits 10`,
		Args: createArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			reveal, _ := cmd.Flags().GetBool("show-secrets")
			expiry := drive.ShareExpiryPermanent
			if cmd.Flags().Changed("expires") {
				parsed, err := parseShareExpiry(cliruntime.FlagStringValue(cmd, "expires"))
				if err != nil {
					return rt.UsageError(cmd, "%s", err.Error())
				}
				expiry = parsed
			}
			passcode, generate, err := resolvePasscode(rt, cmd)
			if err != nil {
				return err
			}
			visitLimit, _ := cmd.Flags().GetInt("limit-visits")
			if visitLimit < 0 {
				return rt.UsageError(cmd, "--limit-visits must not be negative")
			}
			client, err := controlctx.Client(cmd)
			if err != nil {
				return controlctx.FinishError(rt, err)
			}
			created, err := client.CreateShareLink(cmd.Context(), shareapi.LinkCreateRequest{
				Paths:            args,
				Mount:            cliruntime.FlagStringValue(cmd, "mount"),
				Title:            cliruntime.FlagStringValue(cmd, "name"),
				Expiry:           expiry,
				Passcode:         passcode,
				GeneratePasscode: generate,
				VisitLimit:       visitLimit,
			})
			if err != nil {
				return controlctx.FinishError(rt, controlctx.ConnectionError(err, "share"))
			}
			created = shareapi.RedactLink(created, reveal)
			if !reveal && created.Passcode != "" {
				// Keep the hint on stderr so redirected JSON stays valid.
				fmt.Fprintln(cmd.ErrOrStderr(), i18n.FromCommand(cmd).T("out.share.passcode_hidden", created.LinkID))
			}
			if jsonOut {
				return writeJSON(cmd, created)
			}
			return writeShare(cmd, created)
		},
	}
	cmd.Flags().String("mount", "", "confirm the mount named by the paths")
	cmd.Flags().String("name", "", "link title")
	cmd.Flags().String("expires", "forever", "link lifetime: 1d, 7d, 30d, or forever")
	passcodeFlag(cmd)
	cmd.Flags().Int("limit-visits", 0, "maximum number of visits (0 = provider default)")
	cmd.Flags().Bool("show-secrets", false, "print the passcode instead of masking it")
	cmd.Flags().Bool("json", false, "output the link as JSON")
	return controlctx.WithEndpoint(rt, cmd)
}

func newListCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the acting account's links",
		Long: `List links owned by the acting account. Active links are shown by
default; --all also includes expired ones. Use --mount when several mounts are
configured and you do not want the only-mount shortcut.`,
		Example: `  qrypt share list
  qrypt share list --mount quark --all --limit 50`,
		Args: cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			reveal, _ := cmd.Flags().GetBool("show-secrets")
			all, _ := cmd.Flags().GetBool("all")
			limit, _ := cmd.Flags().GetInt("limit")
			client, err := controlctx.Client(cmd)
			if err != nil {
				return controlctx.FinishError(rt, err)
			}
			page, err := client.ListShareLinks(cmd.Context(), shareapi.LinkListRequest{
				Mount:          cliruntime.FlagStringValue(cmd, "mount"),
				Cursor:         cliruntime.FlagStringValue(cmd, "cursor"),
				Limit:          limit,
				IncludeExpired: all,
			})
			if err != nil {
				return controlctx.FinishError(rt, controlctx.ConnectionError(err, "share"))
			}
			page.Shares = shareapi.RedactLinks(page.Shares, reveal)
			if jsonOut {
				return writeJSON(cmd, page)
			}
			return writeShareList(cmd, page)
		},
	}
	cmd.Flags().String("mount", "", "mount to act on when several are configured")
	cmd.Flags().Bool("all", false, "include expired links")
	cmd.Flags().String("cursor", "", "continue from a previous page")
	cmd.Flags().Int("limit", 20, "maximum number of links")
	cmd.Flags().Bool("show-secrets", false, "print passcodes instead of masking them")
	cmd.Flags().Bool("json", false, "output as JSON")
	return controlctx.WithEndpoint(rt, cmd)
}

func newShowCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show LINK_ID",
		Short: "Show one link",
		Long: `Show one link by its qrypt-visible link id. Passcodes are masked unless
--show-secrets is given.`,
		Example: `  qrypt share show a1b2c3d4e5f6
  qrypt share show a1b2c3d4e5f6 --show-secrets --json`,
		Args: linkIDArgs(rt, "show"),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			reveal, _ := cmd.Flags().GetBool("show-secrets")
			client, err := controlctx.Client(cmd)
			if err != nil {
				return controlctx.FinishError(rt, err)
			}
			share, err := client.GetShareLink(cmd.Context(), cliruntime.FlagStringValue(cmd, "mount"), args[0])
			if err != nil {
				return controlctx.FinishError(rt, controlctx.ConnectionError(err, "share"))
			}
			share = shareapi.RedactLink(share, reveal)
			if jsonOut {
				return writeJSON(cmd, share)
			}
			return writeShare(cmd, share)
		},
	}
	cmd.Flags().String("mount", "", "mount to act on when several are configured")
	cmd.Flags().Bool("show-secrets", false, "print the passcode instead of masking it")
	cmd.Flags().Bool("json", false, "output the link as JSON")
	return controlctx.WithEndpoint(rt, cmd)
}

func newUpdateCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update LINK_ID",
		Short: "Change one link's title, expiry, or passcode",
		Long: `Change a link's policy without touching its content. At least one of
--name, --expires, --passcode, or --public is required.`,
		Example: `  qrypt share update a1b2c3d4e5f6 --expires 30d
  qrypt share update a1b2c3d4e5f6 --passcode --name release
  qrypt share update a1b2c3d4e5f6 --public`,
		Args: linkIDArgs(rt, "update"),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			reveal, _ := cmd.Flags().GetBool("show-secrets")
			req := shareapi.LinkUpdateRequest{ID: args[0], Mount: cliruntime.FlagStringValue(cmd, "mount")}
			changed := false
			if cmd.Flags().Changed("name") {
				title := cliruntime.FlagStringValue(cmd, "name")
				req.Title = &title
				changed = true
			}
			if cmd.Flags().Changed("expires") {
				parsed, err := parseShareExpiry(cliruntime.FlagStringValue(cmd, "expires"))
				if err != nil {
					return rt.UsageError(cmd, "%s", err.Error())
				}
				req.Expiry = &parsed
				changed = true
			}
			if cmd.Flags().Changed("passcode") {
				passcode, generate, err := resolvePasscode(rt, cmd)
				if err != nil {
					return err
				}
				if generate {
					generated, err := generatePasscode()
					if err != nil {
						return controlctx.FinishError(rt, err)
					}
					passcode = generated
				}
				req.Passcode = &passcode
				changed = true
			}
			if cmd.Flags().Changed("public") {
				public, _ := cmd.Flags().GetBool("public")
				req.Public = &public
				changed = true
			}
			if !changed {
				return rt.UsageError(cmd, "nothing to update\n\nPass at least one of --name, --expires, --passcode, or --public.")
			}
			client, err := controlctx.Client(cmd)
			if err != nil {
				return controlctx.FinishError(rt, err)
			}
			updated, err := client.UpdateShareLink(cmd.Context(), req)
			if err != nil {
				return controlctx.FinishError(rt, controlctx.ConnectionError(err, "share"))
			}
			updated = shareapi.RedactLink(updated, reveal)
			if !reveal && req.Passcode != nil && *req.Passcode != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), i18n.FromCommand(cmd).T("out.share.passcode_hidden", updated.LinkID))
			}
			if jsonOut {
				return writeJSON(cmd, updated)
			}
			return writeShare(cmd, updated)
		},
	}
	cmd.Flags().String("mount", "", "mount to act on when several are configured")
	cmd.Flags().String("name", "", "new link title")
	cmd.Flags().String("expires", "", "new lifetime: 1d, 7d, 30d, or forever")
	passcodeFlag(cmd)
	cmd.Flags().Bool("public", false, "drop the passcode and publish a public link")
	cmd.Flags().Bool("show-secrets", false, "print the passcode instead of masking it")
	cmd.Flags().Bool("json", false, "output the updated link as JSON")
	return controlctx.WithEndpoint(rt, cmd)
}

func newRemoveCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove LINK_ID...",
		Short: "Remove one or more links",
		Long: `Remove links by their qrypt-visible ids. The provider's management id
is resolved internally; the linked content is not deleted.`,
		Example: `  qrypt share remove a1b2c3d4e5f6
  qrypt share remove a1b2c3d4e5f6 b2c3d4e5f6a1`,
		Args: removeArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			client, err := controlctx.Client(cmd)
			if err != nil {
				return controlctx.FinishError(rt, err)
			}
			mount := cliruntime.FlagStringValue(cmd, "mount")
			removed := make([]string, 0, len(args))
			for _, id := range args {
				if err := client.RemoveShareLink(cmd.Context(), mount, id); err != nil {
					return controlctx.FinishError(rt, controlctx.ConnectionError(err, "share"))
				}
				removed = append(removed, id)
			}
			if jsonOut {
				return writeJSON(cmd, map[string]any{"schema_version": 1, "removed": removed})
			}
			loc := i18n.FromCommand(cmd)
			for _, id := range removed {
				fmt.Fprintln(cmd.OutOrStdout(), loc.T("out.share.removed", id))
			}
			return nil
		},
	}
	cmd.Flags().String("mount", "", "mount to act on when several are configured")
	cmd.Flags().Bool("json", false, "output the removed ids as JSON")
	return controlctx.WithEndpoint(rt, cmd)
}
