package offline

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

func writeTask(cmd *cobra.Command, jsonOut bool, item task.Task) error {
	if jsonOut {
		return util.WritePrettyJSON(cmd.OutOrStdout(), item)
	}
	return writeTaskText(cmd, item)
}

func writeTaskText(cmd *cobra.Command, item task.Task) error {
	out := cmd.OutOrStdout()
	loc := i18n.FromCommand(cmd)
	fmt.Fprintln(out, loc.T("out.offline.id", item.ID))
	fmt.Fprintln(out, loc.T("out.offline.type", item.Type))
	fmt.Fprintln(out, loc.T("out.offline.state", item.State))
	if item.Mount != "" {
		fmt.Fprintln(out, loc.T("out.offline.mount", item.Mount))
	}
	if item.Name != "" {
		fmt.Fprintln(out, loc.T("out.offline.name", item.Name))
	}
	detail := control.OfflineTaskDetail(item)
	if detail != nil {
		if phase := control.OfflineDetailString(detail, "phase"); phase != "" {
			fmt.Fprintln(out, loc.T("out.offline.phase", phase))
		}
		if source := control.OfflineDetailString(detail, "source"); source != "" {
			fmt.Fprintln(out, loc.T("out.offline.source", source))
		}
		if providerID := control.OfflineDetailString(detail, "provider_id"); providerID != "" {
			fmt.Fprintln(out, loc.T("out.offline.provider_id", providerID))
		}
		if dir := control.OfflineDetailString(detail, "parse_dir"); dir != "" {
			fmt.Fprintln(out, loc.T("out.offline.parse_dir", dir))
		}
		if downloadTo := control.OfflineDetailString(detail, "download_to"); downloadTo != "" {
			fmt.Fprintln(out, loc.T("out.offline.download_to", downloadTo))
		}
		if files := offlineDetailFiles(detail); len(files) > 0 {
			fmt.Fprintln(out, loc.T("out.offline.result_files", len(files)))
			for _, name := range files {
				fmt.Fprintf(out, "  - %s\n", name)
			}
		}
	}
	for _, warning := range offlineWarnings(item) {
		fmt.Fprintln(out, loc.T("out.offline.warning", warning))
	}
	if item.Error != nil {
		fmt.Fprintln(out, loc.T("out.offline.error", item.Error.Message))
	}
	return nil
}

func offlineWarnings(item task.Task) []string {
	if item.Detail == nil {
		return nil
	}
	raw, ok := item.Detail["warnings"].([]any)
	if !ok {
		if direct, ok := item.Detail["warnings"].([]string); ok {
			return direct
		}
		return nil
	}
	warnings := make([]string, 0, len(raw))
	for _, entry := range raw {
		if text, ok := entry.(string); ok {
			warnings = append(warnings, text)
		}
	}
	return warnings
}

func offlineDetailFiles(detail map[string]any) []string {
	raw, ok := detail["result_files"].([]any)
	if !ok {
		return nil
	}
	files := make([]string, 0, len(raw))
	for _, entry := range raw {
		file, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := file["name"].(string)
		if name != "" {
			files = append(files, name)
		}
	}
	return files
}

func writeTaskList(cmd *cobra.Command, tasks []task.Task) error {
	out := cmd.OutOrStdout()
	if len(tasks) == 0 {
		fmt.Fprintln(out, i18n.FromCommand(cmd).T("out.offline.empty"))
		return nil
	}
	fmt.Fprintf(out, "%-28s %-16s %-12s %-16s %s\n", "ID", "TYPE", "STATE", "MOUNT", "NAME")
	for _, item := range tasks {
		fmt.Fprintf(out, "%-28s %-16s %-12s %-16s %s\n", item.ID, item.Type, item.State, item.Mount, item.Name)
	}
	return nil
}

func writePreview(cmd *cobra.Command, preview control.OfflinePreviewResult) error {
	out := cmd.OutOrStdout()
	loc := i18n.FromCommand(cmd)
	fmt.Fprintln(out, loc.T("out.offline.preview.source", preview.Source))
	if preview.Title != "" {
		fmt.Fprintln(out, loc.T("out.offline.preview.title", preview.Title))
	}
	if preview.TotalSize > 0 {
		fmt.Fprintln(out, loc.T("out.offline.preview.total_size", cliruntime.FormatBytes(preview.TotalSize)))
	}
	fmt.Fprintln(out, loc.T("out.offline.preview.instant_save", preview.InstantSave))
	if preview.Mount != "" {
		fmt.Fprintln(out, loc.T("out.offline.preview.mount", preview.Mount))
	}
	if !preview.ParseUsable {
		fmt.Fprintln(out, loc.T("out.offline.preview.parse_destination", preview.ParseError))
	}
	if len(preview.Files) > 0 {
		fmt.Fprintln(out, loc.T("out.offline.preview.files"))
		for _, file := range preview.Files {
			marker := " "
			if file.Selected {
				marker = "*"
			}
			fmt.Fprintf(out, "  [%s] %s (%s)\n", marker, file.Name, cliruntime.FormatBytes(file.Size))
		}
	}
	return nil
}
