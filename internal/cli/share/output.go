package share

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// generatePasscode asks the Core's generator for a link passcode. The alphabet
// omits visually ambiguous characters.
func generatePasscode() (string, error) {
	return core.GenerateSharePasscode()
}

func writeJSON(cmd *cobra.Command, value any) error {
	return util.WritePrettyJSON(cmd.OutOrStdout(), value)
}

func writeShare(cmd *cobra.Command, share drive.Share) error {
	out := cmd.OutOrStdout()
	loc := i18n.FromCommand(cmd)
	fmt.Fprintln(out, loc.T("out.share.id", share.LinkID))
	if share.URL != "" {
		fmt.Fprintln(out, loc.T("out.share.url", share.URL))
	}
	if share.Title != "" {
		fmt.Fprintln(out, loc.T("out.share.title", share.Title))
	}
	fmt.Fprintln(out, loc.T("out.share.access", accessLabel(share)))
	fmt.Fprintln(out, loc.T("out.share.expiry", expiryLabel(share.ExpiresAt.IsZero(), share.ExpiresAt.Format("2006-01-02 15:04"))))
	if share.EntryCount > 0 {
		fmt.Fprintln(out, loc.T("out.share.entries", share.EntryCount))
	}
	if share.Size > 0 {
		fmt.Fprintln(out, loc.T("out.share.size", cliruntime.FormatBytes(share.Size)))
	}
	if share.VisitLimit > 0 {
		fmt.Fprintln(out, loc.T("out.share.visits_limited", share.VisitCount, share.VisitLimit))
	} else if share.VisitCount > 0 {
		fmt.Fprintln(out, loc.T("out.share.visits", share.VisitCount))
	}
	if share.Risk != "" {
		fmt.Fprintln(out, loc.T("out.share.risk", share.Risk))
	}
	return nil
}

func accessLabel(share drive.Share) string {
	if share.Public || share.Passcode == "" {
		return "public"
	}
	return "passcode " + share.Passcode
}

func expiryLabel(permanent bool, formatted string) string {
	if permanent {
		return "never"
	}
	return formatted
}

func writeShareList(cmd *cobra.Command, page shareapi.LinkList) error {
	out := cmd.OutOrStdout()
	loc := i18n.FromCommand(cmd)
	if len(page.Shares) == 0 {
		fmt.Fprintln(out, loc.T("out.share.empty"))
		return nil
	}
	fmt.Fprintf(out, "%-14s %-10s %-18s %-6s %-8s %s\n", "ID", "ACCESS", "EXPIRES", "FILES", "SIZE", "URL")
	for _, share := range page.Shares {
		access := "public"
		if !share.Public && share.Passcode != "" {
			access = "passcode"
		}
		expires := "never"
		if !share.ExpiresAt.IsZero() {
			expires = share.ExpiresAt.Format("2006-01-02 15:04")
		}
		size := "-"
		if share.Size > 0 {
			size = cliruntime.FormatBytes(share.Size)
		}
		fmt.Fprintf(out, "%-14s %-10s %-18s %-6d %-8s %s\n",
			share.LinkID, access, expires, share.EntryCount, size, share.URL)
	}
	if strings.TrimSpace(page.Next) != "" {
		fmt.Fprintln(out, loc.T("out.share.next", page.Next))
	}
	return nil
}
