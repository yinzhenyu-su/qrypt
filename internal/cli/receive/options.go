package receive

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/controlctx"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
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

// stdinIsTerminal reports whether the command's stdin is an interactive
// terminal. Tests override it to exercise the prompt deterministically.
var stdinIsTerminal = func(cmd *cobra.Command) bool {
	file, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// resolveReceiveMount chooses the acting mount when the caller did not pass
// --mount (and, for save/fetch, --to). The Core reports which mounts recognize
// the link; one candidate is used directly unless requireExplicit is set (save
// treats a mount as its root, so the choice must be deliberate). Several
// candidates are offered interactively; non-interactive callers get a usage
// error naming the missing flag.
func resolveReceiveMount(rt cliruntime.Runtime, cmd *cobra.Command, client *control.Client, link string, requireExplicit bool) (string, error) {
	if mount := strings.TrimSpace(cliruntime.FlagStringValue(cmd, "mount")); mount != "" {
		return mount, nil
	}
	if strings.TrimSpace(cliruntime.FlagStringValue(cmd, "to")) != "" {
		return "", nil
	}
	result, err := client.ResolveShareLink(cmd.Context(), shareapi.ResolveRequest{Link: link})
	if err != nil {
		// A Core started from an older build has no /share/resolve route. Fall
		// back to the command's own request so a single mount still works and an
		// ambiguous one reports its candidates, while telling the user why the
		// interactive choice did not appear.
		if isMissingResolveRoute(err) {
			fmt.Fprintln(cmd.ErrOrStderr(), i18n.FromCommand(cmd).T("out.receive.mounts.core_stale"))
			return "", nil
		}
		return "", controlctx.FinishError(rt, controlctx.ConnectionError(err, "receive"))
	}
	switch {
	case len(result.Candidates) == 0:
		// Let the actual command report share_link_invalid with its own detail.
		return "", nil
	case len(result.Candidates) == 1 && !requireExplicit:
		return result.Candidates[0], nil
	}

	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut || !stdinIsTerminal(cmd) {
		return "", mountChoiceError(rt, cmd, link, result.Candidates, requireExplicit)
	}
	return promptMount(rt, cmd, link, result.Candidates, requireExplicit)
}

func mountChoiceError(rt cliruntime.ErrorFactory, cmd *cobra.Command, link string, candidates []string, requireExplicit bool) error {
	loc := i18n.FromCommand(cmd)
	names := strings.Join(candidates, ", ")
	if requireExplicit {
		return rt.UsageError(cmd, "%s\n\n%s", loc.T("out.receive.mounts.ambiguous", names),
			loc.T("out.receive.mounts.choose_destination", link, candidates[0], link, candidates[0]))
	}
	return rt.UsageError(cmd, "%s\n\n%s", loc.T("out.receive.mounts.ambiguous", names),
		loc.T("out.receive.mounts.choose", cmd.Name(), link, candidates[0]))
}

// promptMount asks the user to pick one candidate by number or name. It retries
// a few times so a typo does not force the whole command to be retyped.
func promptMount(rt cliruntime.ErrorFactory, cmd *cobra.Command, link string, candidates []string, requireExplicit bool) (string, error) {
	out := cmd.ErrOrStderr()
	loc := i18n.FromCommand(cmd)
	fmt.Fprintln(out, loc.T("out.receive.mounts.title"))
	for index, name := range candidates {
		fmt.Fprintln(out, loc.T("out.receive.mounts.entry", index+1, name))
	}
	if requireExplicit {
		fmt.Fprintln(out, loc.T("out.receive.mounts.root_hint"))
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	for attempt := 0; attempt < 3; attempt++ {
		fmt.Fprint(out, loc.T("out.receive.mounts.select", len(candidates)))
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			break
		}
		choice := strings.TrimSpace(line)
		if index, convErr := strconv.Atoi(choice); convErr == nil && index >= 1 && index <= len(candidates) {
			return candidates[index-1], nil
		}
		for _, name := range candidates {
			if strings.EqualFold(choice, name) {
				return name, nil
			}
		}
		fmt.Fprintln(out, loc.T("out.receive.mounts.invalid"))
	}
	return "", mountChoiceError(rt, cmd, link, candidates, requireExplicit)
}

// isMissingResolveRoute reports a Core built before /share/resolve existed.
func isMissingResolveRoute(err error) bool {
	var transport *control.TransportError
	return errors.As(err, &transport) && transport.Status == http.StatusNotFound
}

// finishReceiveCall maps a receive failure to the process exit code and, when
// the provider asked for a passcode, tells the user which flag supplies it.
// That is the next step after the mount has been chosen.
func finishReceiveCall(rt cliruntime.Runtime, cmd *cobra.Command, err error) error {
	if err != nil && passcodeRequired(err) {
		fmt.Fprintln(cmd.ErrOrStderr(), i18n.FromCommand(cmd).T("out.receive.passcode_hint"))
	}
	return controlctx.FinishError(rt, controlctx.ConnectionError(err, "receive"))
}

func passcodeRequired(err error) bool {
	var transport *control.TransportError
	return errors.As(err, &transport) && strings.Contains(transport.Body, string(drive.ShareErrPasscodeRequired))
}

// promptPasscode asks for the link passcode after the mount has been chosen.
// The input is echoed, like the existing bare --passcode stdin path; callers
// that need secrecy can pass --passcode=CODE or pipe a line.
func promptPasscode(rt cliruntime.ErrorFactory, cmd *cobra.Command) (string, error) {
	fmt.Fprint(cmd.ErrOrStderr(), i18n.FromCommand(cmd).T("out.receive.passcode_prompt"))
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return "", rt.UsageError(cmd, "a passcode is required")
	}
	value := strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(value) == "" {
		return "", rt.UsageError(cmd, "a passcode is required")
	}
	return value, nil
}
