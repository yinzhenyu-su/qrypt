package offline

import (
	"errors"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// usageRecorder captures usage-error text produced by the argument validators.
type usageRecorder struct{ messages []string }

func (u *usageRecorder) UsageError(cmd *cobra.Command, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	u.messages = append(u.messages, message)
	return errors.New(message)
}

func (u *usageRecorder) ExitError(_ int, err error) error { return err }

func TestSourceArgsHints(t *testing.T) {
	t.Parallel()

	recorder := &usageRecorder{}
	validate := sourceArgs(recorder, "add")
	cmd := &cobra.Command{Use: "add SOURCE"}

	if err := validate(cmd, nil); err == nil {
		t.Fatal("expected a missing-SOURCE error")
	}
	last := recorder.messages[len(recorder.messages)-1]
	for _, want := range []string{"missing SOURCE", "magnet link", "qrypt offline add"} {
		if !strings.Contains(last, want) {
			t.Fatalf("missing-SOURCE hint %q lacks %q", last, want)
		}
	}
	if err := validate(cmd, []string{"a", "b"}); err == nil {
		t.Fatal("expected a too-many-arguments error")
	}
	if err := validate(cmd, []string{"magnet:?xt=urn:btih:abc"}); err != nil {
		t.Fatalf("one source should validate: %v", err)
	}
}

func TestTaskIDArgsHints(t *testing.T) {
	t.Parallel()

	recorder := &usageRecorder{}
	validate := taskIDArgs(recorder)
	cmd := &cobra.Command{Use: "show TASK_ID"}

	if err := validate(cmd, nil); err == nil {
		t.Fatal("expected a missing-TASK_ID error")
	}
	last := recorder.messages[len(recorder.messages)-1]
	for _, want := range []string{"missing TASK_ID", "qrypt offline list"} {
		if !strings.Contains(last, want) {
			t.Fatalf("missing-TASK_ID hint %q lacks %q", last, want)
		}
	}
}

// TestPrepareOfflineSourceReadsRelativeTorrentInTheCLI pins client-side
// normalization: a relative .torrent path must be read in the caller's working
// directory before the source crosses the daemon boundary.
func TestPrepareOfflineSourceReadsRelativeTorrentInTheCLI(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".offline-source-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	info := []byte("d4:name4:test6:lengthi5ee")
	torrent := append([]byte("d8:announce16:http://tracker/x4:info"), info...)
	torrent = append(torrent, 'e')
	path := filepath.Join(dir, "example.torrent")
	if err := os.WriteFile(path, torrent, 0o644); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(".", path)
	if err != nil {
		t.Fatal(err)
	}

	recorder := &usageRecorder{}
	source, kind, err := prepareOfflineSource(recorder, &cobra.Command{Use: "add"}, relative)
	if err != nil {
		t.Fatalf("prepareOfflineSource: %v", err)
	}
	if !strings.HasPrefix(source, "magnet:?xt=urn:btih:") {
		t.Fatalf("normalized source = %q, want a magnet", source)
	}
	if !strings.Contains(source, "dn=test") {
		t.Fatalf("normalized source lost the torrent name: %q", source)
	}
	if kind != "magnet" {
		t.Fatalf("kind = %q, want magnet", kind)
	}
}

// TestOfflineProgressRenderer pins the shared progress renderer: it draws to its
// writer (stderr), skips identical redraws, and terminates the line on finish.
func TestOfflineProgressRenderer(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	progress := newOfflineProgress(&buf, i18n.New(i18n.LangEN))
	item := task.Task{
		State: task.StateRunning,
		Progress: task.Progress{
			Phase:              task.PhaseDownload,
			TransferBytesDone:  512,
			TransferBytesTotal: 1024,
			ItemsDone:          1,
			ItemsTotal:         2,
		},
	}
	progress.render(item)
	first := buf.String()
	if !strings.Contains(first, "download") || !strings.Contains(first, "files 1/2") {
		t.Fatalf("progress line = %q", first)
	}
	progress.render(item)
	if buf.String() != first {
		t.Fatalf("identical progress was redrawn: %q", buf.String())
	}
	progress.finish()
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Fatalf("finish did not terminate the line: %q", buf.String())
	}
}

// TestResolveLocalDestination covers relative, namespace, and absolute inputs.
func TestResolveLocalDestination(t *testing.T) {
	t.Parallel()

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveLocalDestination("./downloads/")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(workingDir, "downloads") + string(filepath.Separator)
	if got != want {
		t.Fatalf("relative directory = %q, want %q", got, want)
	}
	got, err = resolveLocalDestination("out.bin")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(workingDir, "out.bin"); got != want {
		t.Fatalf("relative file = %q, want %q", got, want)
	}
	if got, err := resolveLocalDestination("/MOUNT/dir"); err != nil || got != "/MOUNT/dir" {
		t.Fatalf("namespace path = %q err=%v", got, err)
	}
	absolute := filepath.Join(workingDir, "absolute.bin")
	if got, err := resolveLocalDestination(absolute); err != nil || got != absolute {
		t.Fatalf("absolute path = %q err=%v", got, err)
	}
}

func TestRequireProviderSelectionHint(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "fetch SOURCE"}
	cmd.Flags().String("parse-to", "", "")
	cmd.Flags().String("mount", "", "")
	recorder := &usageRecorder{}

	if err := requireProviderSelection(recorder, cmd); err == nil {
		t.Fatal("expected a provider-selection error")
	}
	last := recorder.messages[len(recorder.messages)-1]
	if !strings.Contains(last, "--parse-to or --mount") {
		t.Fatalf("provider-selection hint = %q", last)
	}

	if err := cmd.Flags().Set("mount", "quark"); err != nil {
		t.Fatal(err)
	}
	if err := requireProviderSelection(recorder, cmd); err != nil {
		t.Fatalf("a provider selector should satisfy the check: %v", err)
	}
}

func selectionCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	addSelectionFlags(cmd)
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse flags %v: %v", args, err)
	}
	return cmd
}

func TestResolveSelection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		args    []string
		want    task.OfflineSelection
		wantErr bool
	}{
		{name: "default is all", args: nil, want: task.OfflineSelection{Mode: task.OfflineSelectionAll}},
		{name: "explicit all", args: []string{"--select", "all"}, want: task.OfflineSelection{Mode: task.OfflineSelectionAll}},
		{name: "largest default count", args: []string{"--select", "largest"}, want: task.OfflineSelection{Mode: task.OfflineSelectionLargest, Largest: 1}},
		{name: "largest n", args: []string{"--select", "largest:3"}, want: task.OfflineSelection{Mode: task.OfflineSelectionLargest, Largest: 3}},
		{name: "include and exclude", args: []string{"--include", "*.mkv", "--exclude", "*sample*"}, want: task.OfflineSelection{Mode: task.OfflineSelectionInclude, Include: []string{"*.mkv"}, Exclude: []string{"*sample*"}}},
		{name: "exclude without include", args: []string{"--exclude", "*.tmp"}, wantErr: true},
		{name: "select conflicts with include", args: []string{"--select", "all", "--include", "*.mkv"}, wantErr: true},
		{name: "unknown mode", args: []string{"--select", "weird"}, wantErr: true},
		{name: "largest without count", args: []string{"--select", "largest:0"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSelection(selectionCommand(t, tc.args...))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSelection: %v", err)
			}
			if got.Mode != tc.want.Mode || got.Largest != tc.want.Largest {
				t.Fatalf("selection = %+v, want %+v", got, tc.want)
			}
			if len(got.Include) != len(tc.want.Include) || len(got.Exclude) != len(tc.want.Exclude) {
				t.Fatalf("patterns = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestCommandSurface asserts the provider-neutral command group: the documented
// subcommands exist, and no command declares a provider/driver/socket flag.
func TestCommandSurface(t *testing.T) {
	t.Parallel()

	// The constructors only wire flags and the endpoint resolver; the runtime
	// is never invoked, so a zero test runtime is enough.
	rt := cliruntime.TestRuntime{}
	add := newAddCommand(rt)
	fetch := newFetchCommand(rt)
	inspect := newInspectCommand(rt)
	list := newListCommand(rt)
	show := newShowCommand(rt)
	cancel := newCancelCommand(rt)
	retry := newRetryCommand(rt)
	wait := newWaitCommand(rt)
	for name, sub := range map[string]*cobra.Command{
		"add": add, "fetch": fetch, "inspect": inspect, "list": list,
		"show": show, "cancel": cancel, "retry": retry, "wait": wait,
	} {
		if sub.Use == "" {
			t.Fatalf("%s has no usage", name)
		}
		for _, forbidden := range []string{"provider", "driver", "socket", "task-id-flag"} {
			if sub.Flags().Lookup(forbidden) != nil {
				t.Fatalf("%s exposes provider-specific flag %q", name, forbidden)
			}
		}
	}
	if fetch.Flags().Lookup("download-to") == nil {
		t.Fatal("fetch must expose --download-to")
	}
	if fetch.Flags().Lookup("keep-cloud") == nil || fetch.Flags().Lookup("no-wait") == nil {
		t.Fatal("fetch must expose --keep-cloud and --no-wait")
	}
	if fetch.Flags().Lookup("wait") == nil || fetch.Flags().Lookup("flat") == nil {
		t.Fatal("fetch must expose --wait and --flat")
	}
	if add.Flags().Lookup("parse-to") == nil && add.Flags().Lookup("mount") == nil {
		t.Fatal("add must expose --parse-to or --mount")
	}
	if list.Flags().Lookup("limit") == nil || list.Flags().Lookup("all") == nil {
		t.Fatal("list must expose --limit and --all")
	}
}

// TestTransferTuning pins the fetch transfer flags: the parse of a size suffix,
// the range checks, and that no flag keeps the Core default.
func TestTransferTuning(t *testing.T) {
	t.Parallel()

	newCommand := func() *cobra.Command {
		cmd := &cobra.Command{Use: "fetch"}
		cmd.Flags().Int("workers", 0, "")
		cmd.Flags().String("chunk-size", "", "")
		return cmd
	}

	cmd := newCommand()
	workers, chunk, err := transferTuning(cmd)
	if err != nil || workers != 0 || chunk != 0 {
		t.Fatalf("defaults = %d/%d err=%v, want 0/0", workers, chunk, err)
	}

	cmd = newCommand()
	_ = cmd.Flags().Set("workers", "8")
	_ = cmd.Flags().Set("chunk-size", "4M")
	workers, chunk, err = transferTuning(cmd)
	if err != nil || workers != 8 || chunk != 4<<20 {
		t.Fatalf("explicit = %d/%d err=%v, want 8/%d", workers, chunk, err, 4<<20)
	}

	invalid := []struct {
		name    string
		workers string
		chunk   string
	}{
		{name: "workers above the maximum", workers: fmt.Sprint(task.MaxOfflineTransferWorkers + 1)},
		{name: "negative workers", workers: "-1"},
		{name: "chunk below the minimum", chunk: "1K"},
		{name: "chunk above the maximum", chunk: "1G"},
		{name: "unparsable chunk", chunk: "eight"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newCommand()
			if tc.workers != "" {
				_ = cmd.Flags().Set("workers", tc.workers)
			}
			if tc.chunk != "" {
				_ = cmd.Flags().Set("chunk-size", tc.chunk)
			}
			if _, _, err := transferTuning(cmd); err == nil {
				t.Fatalf("transferTuning accepted workers=%q chunk=%q", tc.workers, tc.chunk)
			}
		})
	}
}
