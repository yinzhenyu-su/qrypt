package receive

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

func writeJSON(cmd *cobra.Command, value any) error {
	return util.WritePrettyJSON(cmd.OutOrStdout(), value)
}

func writeInspect(cmd *cobra.Command, preview shareapi.InspectResult) error {
	out := cmd.OutOrStdout()
	loc := i18n.FromCommand(cmd)
	if preview.Share.Title != "" {
		fmt.Fprintln(out, loc.T("out.receive.preview.title", preview.Share.Title))
	}
	if preview.Link.URL != "" {
		fmt.Fprintln(out, loc.T("out.receive.preview.link", preview.Link.URL))
	}
	if preview.Share.Owner.Name != "" {
		fmt.Fprintln(out, loc.T("out.receive.preview.owner", preview.Share.Owner.Name))
	}
	access := "public"
	if preview.Share.Passcode != "" || !preview.Share.Public {
		access = "passcode"
	}
	fmt.Fprintln(out, loc.T("out.receive.preview.access", access))
	if !preview.Share.ExpiresAt.IsZero() {
		fmt.Fprintln(out, loc.T("out.receive.preview.expires", preview.Share.ExpiresAt.Format("2006-01-02 15:04")))
	}
	if len(preview.Entries) == 0 {
		fmt.Fprintln(out, loc.T("out.receive.preview.empty"))
		return nil
	}
	fmt.Fprintln(out, loc.T("out.receive.preview.entries", len(preview.Entries)))
	for _, entry := range preview.Entries {
		marker := " "
		if entry.IsDir {
			marker = "d"
		}
		fmt.Fprintf(out, "  [%s] %-12s %10s  %s\n", marker, entry.ID, cliruntime.FormatBytes(entry.Size), entry.Name)
	}
	if preview.Next != "" {
		fmt.Fprintln(out, loc.T("out.receive.preview.next", preview.Next))
	}
	return nil
}

func writeTask(cmd *cobra.Command, jsonOut, revealSecrets bool, item task.Task) error {
	item = shareapi.RedactTask(item, revealSecrets)
	if jsonOut {
		return writeJSON(cmd, item)
	}
	return writeTaskText(cmd, item)
}

func writeTaskText(cmd *cobra.Command, item task.Task) error {
	out := cmd.OutOrStdout()
	loc := i18n.FromCommand(cmd)
	fmt.Fprintln(out, loc.T("out.receive.id", item.ID))
	fmt.Fprintln(out, loc.T("out.receive.type", item.Type))
	fmt.Fprintln(out, loc.T("out.receive.state", item.State))
	if item.Mount != "" {
		fmt.Fprintln(out, loc.T("out.receive.mount", item.Mount))
	}
	detail := shareapi.TaskDetail(item)
	if detail != nil {
		if phase := shareapi.DetailString(detail, "phase"); phase != "" {
			fmt.Fprintln(out, loc.T("out.receive.phase", phase))
		}
		if link := shareapi.DetailString(detail, "link"); link != "" {
			fmt.Fprintln(out, loc.T("out.receive.link", link))
		}
		if linkID := shareapi.DetailString(detail, "link_id"); linkID != "" {
			fmt.Fprintln(out, loc.T("out.receive.link_id", linkID))
		}
		if passcode := shareapi.DetailString(detail, "passcode"); passcode != "" {
			fmt.Fprintln(out, loc.T("out.receive.passcode", passcode))
		}
		if toDir := shareapi.DetailString(detail, "to_dir"); toDir != "" {
			mount := shareapi.DetailString(detail, "to_mount")
			fmt.Fprintln(out, loc.T("out.receive.to", mountQualified(mount, toDir)))
		}
		if downloadTo := shareapi.DetailString(detail, "download_to"); downloadTo != "" {
			fmt.Fprintln(out, loc.T("out.receive.download_to", downloadTo))
		}
		if handle := shareapi.DetailString(detail, "save_handle"); handle != "" {
			fmt.Fprintln(out, loc.T("out.receive.save_handle", handle))
		}
		if saved := detailEntries(detail, "saved"); len(saved) > 0 {
			fmt.Fprintln(out, loc.T("out.receive.saved", len(saved)))
			for _, entry := range saved {
				fmt.Fprintf(out, "  + %s (%s)\n", entry.name, cliruntime.FormatBytes(entry.size))
			}
		}
		if transferred := detailEntries(detail, "transferred"); len(transferred) > 0 {
			fmt.Fprintln(out, loc.T("out.receive.transferred", len(transferred)))
			for _, entry := range transferred {
				fmt.Fprintf(out, "  - %s (%s)\n", entry.name, cliruntime.FormatBytes(entry.size))
			}
		}
		for _, failed := range detailFailures(detail) {
			fmt.Fprintln(out, loc.T("out.receive.entry_error", failed.name, failed.code))
		}
		for _, warning := range detailWarnings(detail) {
			fmt.Fprintln(out, loc.T("out.receive.warning", warning))
		}
	}
	if item.Error != nil {
		fmt.Fprintln(out, loc.T("out.receive.error", item.Error.Message))
	}
	return nil
}

// mountQualified renders a mount-relative directory as its mount-qualified
// form for display.
func mountQualified(mount, dir string) string {
	dir = strings.TrimSpace(dir)
	if mount == "" {
		return dir
	}
	if dir == "" || dir == "/" {
		return "/" + mount
	}
	return "/" + mount + "/" + strings.TrimPrefix(dir, "/")
}

func writeTaskList(cmd *cobra.Command, tasks []task.Task) error {
	out := cmd.OutOrStdout()
	loc := i18n.FromCommand(cmd)
	if len(tasks) == 0 {
		fmt.Fprintln(out, loc.T("out.receive.empty"))
		return nil
	}
	fmt.Fprintf(out, "%-24s %-12s %-16s %-14s %s\n", "ID", "TYPE", "STATE", "MOUNT", "LINK")
	for _, item := range tasks {
		detail := shareapi.TaskDetail(item)
		link := shareapi.DetailString(detail, "link_id")
		if link == "" {
			link = shareapi.DetailString(detail, "link")
		}
		fmt.Fprintf(out, "%-24s %-12s %-16s %-14s %s\n", item.ID, item.Type, item.State, item.Mount, link)
	}
	return nil
}

type detailEntry struct {
	name string
	size int64
	code string
}

func detailEntries(detail map[string]any, key string) []detailEntry {
	raw, ok := detail[key].([]any)
	if !ok {
		return nil
	}
	out := make([]detailEntry, 0, len(raw))
	for _, value := range raw {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		item := detailEntry{}
		item.name, _ = entry["name"].(string)
		item.code, _ = entry["error_code"].(string)
		switch size := entry["size"].(type) {
		case float64:
			item.size = int64(size)
		case int64:
			item.size = size
		}
		if item.code == "" && item.name != "" {
			out = append(out, item)
		}
	}
	return out
}

func detailFailures(detail map[string]any) []detailEntry {
	var out []detailEntry
	for _, key := range []string{"saved", "transferred"} {
		raw, ok := detail[key].([]any)
		if !ok {
			continue
		}
		for _, value := range raw {
			entry, ok := value.(map[string]any)
			if !ok {
				continue
			}
			code, _ := entry["error_code"].(string)
			if code == "" {
				continue
			}
			name, _ := entry["name"].(string)
			if name == "" {
				name, _ = entry["source_id"].(string)
			}
			out = append(out, detailEntry{name: name, code: code})
		}
	}
	return out
}

func detailWarnings(detail map[string]any) []string {
	raw, ok := detail["warnings"].([]any)
	if !ok {
		if direct, ok := detail["warnings"].([]string); ok {
			return direct
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, ok := value.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}
