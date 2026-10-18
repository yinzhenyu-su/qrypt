package offline

import (
	"fmt"

	"github.com/spf13/cobra"
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
	fmt.Fprintf(out, "id: %s\n", item.ID)
	fmt.Fprintf(out, "type: %s\n", item.Type)
	fmt.Fprintf(out, "state: %s\n", item.State)
	if item.Mount != "" {
		fmt.Fprintf(out, "mount: %s\n", item.Mount)
	}
	if item.Name != "" {
		fmt.Fprintf(out, "name: %s\n", item.Name)
	}
	detail := control.OfflineTaskDetail(item)
	if detail != nil {
		if phase := control.OfflineDetailString(detail, "phase"); phase != "" {
			fmt.Fprintf(out, "phase: %s\n", phase)
		}
		if source := control.OfflineDetailString(detail, "source"); source != "" {
			fmt.Fprintf(out, "source: %s\n", source)
		}
		if providerID := control.OfflineDetailString(detail, "provider_id"); providerID != "" {
			fmt.Fprintf(out, "provider_id: %s\n", providerID)
		}
		if dir := control.OfflineDetailString(detail, "parse_dir"); dir != "" {
			fmt.Fprintf(out, "parse_dir: %s\n", dir)
		}
		if downloadTo := control.OfflineDetailString(detail, "download_to"); downloadTo != "" {
			fmt.Fprintf(out, "download_to: %s\n", downloadTo)
		}
		if files := offlineDetailFiles(detail); len(files) > 0 {
			fmt.Fprintf(out, "result_files: %d\n", len(files))
			for _, name := range files {
				fmt.Fprintf(out, "  - %s\n", name)
			}
		}
	}
	for _, warning := range offlineWarnings(item) {
		fmt.Fprintf(out, "warning: %s\n", warning)
	}
	if item.Error != nil {
		fmt.Fprintf(out, "error: %s\n", item.Error.Message)
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
		fmt.Fprintln(out, "no offline tasks")
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
	fmt.Fprintf(out, "source: %s\n", preview.Source)
	if preview.Title != "" {
		fmt.Fprintf(out, "title: %s\n", preview.Title)
	}
	if preview.TotalSize > 0 {
		fmt.Fprintf(out, "total_size: %s\n", cliruntime.FormatBytes(preview.TotalSize))
	}
	fmt.Fprintf(out, "instant_save: %t\n", preview.InstantSave)
	if preview.Mount != "" {
		fmt.Fprintf(out, "mount: %s\n", preview.Mount)
	}
	if !preview.ParseUsable {
		fmt.Fprintf(out, "parse_destination: unusable (%s)\n", preview.ParseError)
	}
	if len(preview.Files) > 0 {
		fmt.Fprintln(out, "files:")
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
