package receive

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// passcodeFromStdin is the sentinel cobra stores for a bare `--passcode`.
const passcodeFromStdin = "\x00stdin"

// passcodeFlag adds a --passcode flag whose bare form reads one line from
// stdin, keeping the secret out of shell history.
func passcodeFlag(cmd *cobra.Command) {
	cmd.Flags().String("passcode", "", "link passcode; omit the value to read one line from stdin")
	cmd.Flags().Lookup("passcode").NoOptDefVal = passcodeFromStdin
}

// resolvePasscode returns the passcode for a link. A link's own ?pwd= query is
// left in the link when no flag is given, so the Core's parser resolves it.
func resolvePasscode(rt cliruntime.ErrorFactory, cmd *cobra.Command) (string, error) {
	if !cmd.Flags().Changed("passcode") {
		return "", nil
	}
	raw, _ := cmd.Flags().GetString("passcode")
	if raw == passcodeFromStdin {
		reader := bufio.NewReader(cmd.InOrStdin())
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return "", rt.UsageError(cmd, "--passcode reads one line from stdin, but stdin was empty")
		}
		raw = line
	}
	raw = strings.TrimRight(raw, "\r\n")
	if strings.TrimSpace(raw) == "" {
		return "", rt.UsageError(cmd, "--passcode value must not be empty")
	}
	return raw, nil
}

// resolveSelection builds the provider-neutral selection from the shared
// selector flags.
func resolveSelection(cmd *cobra.Command) (task.OfflineSelection, error) {
	selectValue := strings.TrimSpace(cliruntime.FlagStringValue(cmd, "select"))
	include, _ := cmd.Flags().GetStringArray("include")
	exclude, _ := cmd.Flags().GetStringArray("exclude")

	if selectValue != "" && (len(include) > 0 || len(exclude) > 0) {
		return task.OfflineSelection{}, fmt.Errorf("--select cannot be combined with --include/--exclude")
	}
	switch {
	case selectValue == "" || selectValue == "all":
		if selectValue == "" && (len(include) > 0 || len(exclude) > 0) {
			if len(include) == 0 {
				return task.OfflineSelection{}, fmt.Errorf("--exclude requires --include")
			}
			return task.OfflineSelection{Mode: task.OfflineSelectionInclude, Include: include, Exclude: exclude}, nil
		}
		if selectValue == "all" && (len(include) > 0 || len(exclude) > 0) {
			return task.OfflineSelection{}, fmt.Errorf("--select all cannot be combined with --include/--exclude")
		}
		return task.OfflineSelection{Mode: task.OfflineSelectionAll}, nil
	case selectValue == "largest":
		return task.OfflineSelection{Mode: task.OfflineSelectionLargest, Largest: 1}, nil
	case strings.HasPrefix(selectValue, "largest:"):
		count, err := strconv.Atoi(strings.TrimPrefix(selectValue, "largest:"))
		if err != nil || count < 1 {
			return task.OfflineSelection{}, fmt.Errorf("--select largest:N requires a positive N")
		}
		return task.OfflineSelection{Mode: task.OfflineSelectionLargest, Largest: count}, nil
	default:
		return task.OfflineSelection{}, fmt.Errorf("unsupported --select value %q (use all or largest[:N])", selectValue)
	}
}

func addSelectionFlags(cmd *cobra.Command) {
	cmd.Flags().String("select", "", "file selection: all or largest[:N]")
	cmd.Flags().StringArray("include", nil, "include file name glob (repeatable)")
	cmd.Flags().StringArray("exclude", nil, "exclude file name glob (repeatable)")
}

// transferTuning resolves the fetch transfer flags. Zero keeps the Core default.
func transferTuning(cmd *cobra.Command) (workers int, chunkSize int64, err error) {
	workers, _ = cmd.Flags().GetInt("workers")
	if workers < 0 || workers > task.MaxOfflineTransferWorkers {
		return 0, 0, fmt.Errorf("--workers must be between 1 and %d (0 uses the default %d)", task.MaxOfflineTransferWorkers, task.DefaultOfflineTransferWorkers)
	}
	raw := strings.TrimSpace(cliruntime.FlagStringValue(cmd, "chunk-size"))
	if raw == "" {
		return workers, 0, nil
	}
	chunkSize, err = config.ParseSize(raw)
	if err != nil {
		return 0, 0, fmt.Errorf("--chunk-size %q: %w", raw, err)
	}
	switch {
	case chunkSize < task.MinOfflineTransferChunk:
		return 0, 0, fmt.Errorf("--chunk-size must be at least %s", task.OfflineByteSize(task.MinOfflineTransferChunk))
	case chunkSize > task.MaxOfflineTransferChunk:
		return 0, 0, fmt.Errorf("--chunk-size must be at most %s", task.OfflineByteSize(task.MaxOfflineTransferChunk))
	}
	return workers, chunkSize, nil
}

func addTransferFlags(cmd *cobra.Command) {
	cmd.Flags().Int("workers", 0, fmt.Sprintf("concurrent download connections (1-%d, 0 = default %d)", task.MaxOfflineTransferWorkers, task.DefaultOfflineTransferWorkers))
	cmd.Flags().String("chunk-size", "", fmt.Sprintf("bytes each download request covers, e.g. 4M (%s-%s, empty = default %s)", task.OfflineByteSize(task.MinOfflineTransferChunk), task.OfflineByteSize(task.MaxOfflineTransferChunk), task.OfflineByteSize(task.DefaultOfflineTransferChunk)))
	cliruntime.MarkAdvancedFlag(cmd.Flags().Lookup("workers"))
	cliruntime.MarkAdvancedFlag(cmd.Flags().Lookup("chunk-size"))
}
