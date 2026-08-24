package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/syncer"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func NewListCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list [REMOTE]",
		Short:             "List a directory",
		Args:              cliruntime.MaxArgs(rt, 1),
		RunE:              runList(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	cmd.Flags().Bool("jsonl", false, "write JSON Lines output (one entry per line)")
	cmd.Flags().Bool("human", false, "human-readable sizes (K/M/G/T)")
	cmd.Flags().Bool("remote-names", false, "include backend remote names and raw paths")
	return cmd
}

func runList(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		path := "/"
		if len(args) > 0 {
			path = args[0]
		}
		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		entries, err := fs.List(ctx, path)
		if err != nil {
			return err
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		asJSONL, _ := cmd.Flags().GetBool("jsonl")
		if asJSON && asJSONL {
			return rt.UsageError(cmd, "--json and --jsonl cannot be used together")
		}
		includeRemoteNames, _ := cmd.Flags().GetBool("remote-names")
		if asJSONL {
			if entries == nil {
				return nil
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetEscapeHTML(false)
			if includeRemoteNames {
				output, err := ListEntriesWithRemoteNames(ctx, fs, path, entries)
				if err != nil {
					return err
				}
				for _, entry := range output {
					if err := encoder.Encode(entry); err != nil {
						return err
					}
				}
				return nil
			}
			for _, entry := range entries {
				if err := encoder.Encode(entry); err != nil {
					return err
				}
			}
			return nil
		}
		if asJSON {
			if entries == nil {
				entries = []drive.Entry{}
			}
			if includeRemoteNames {
				output, err := ListEntriesWithRemoteNames(ctx, fs, path, entries)
				if err != nil {
					return err
				}
				return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), output)
			}
			return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), entries)
		}
		remoteParent := ""
		if includeRemoteNames {
			remoteParent, err = RemoteParentPath(ctx, fs, path)
			if err != nil {
				return err
			}
		}
		for _, entry := range entries {
			kind := "file"
			if entry.IsDir {
				kind = "dir "
			}
			size := fmt.Sprintf("%10d", entry.Size)
			human, _ := cmd.Flags().GetBool("human")
			if human {
				size = fmt.Sprintf("%10s", cliruntime.FormatBytes(entry.Size))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s", kind, size, entry.Name)
			if includeRemoteNames {
				remoteName, _ := drive.EntryRemoteName(entry)
				fmt.Fprintf(cmd.OutOrStdout(), "\tremote_name=%s\tremote_path=%s", remoteName, JoinRemotePath(remoteParent, remoteName))
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}
		return nil
	}
}

type ListEntry struct {
	drive.Entry
	RemoteName string `json:"remote_name"`
	RemotePath string `json:"remote_path"`
}

func ListEntriesWithRemoteNames(ctx context.Context, fs vfs.Reader, dir string, entries []drive.Entry) ([]ListEntry, error) {
	remoteParent, err := RemoteParentPath(ctx, fs, dir)
	if err != nil {
		return nil, err
	}
	output := make([]ListEntry, 0, len(entries))
	for _, entry := range entries {
		remoteName, _ := drive.EntryRemoteName(entry)
		output = append(output, ListEntry{
			Entry:      entry,
			RemoteName: remoteName,
			RemotePath: JoinRemotePath(remoteParent, remoteName),
		})
	}
	return output, nil
}

func RemoteParentPath(ctx context.Context, fs vfs.Reader, dir string) (string, error) {
	clean := cliruntime.CleanListPath(dir)
	if clean == "/" {
		return "/", nil
	}
	parts := strings.Split(strings.Trim(clean, "/"), "/")
	if len(parts) == 0 {
		return "/", nil
	}
	rawParts := []string{parts[0]}
	resolver, ok := fs.(diagnostics.DebugResolver)
	if !ok {
		return clean, nil
	}
	for i := 1; i < len(parts); i++ {
		prefix := "/" + strings.Join(parts[:i+1], "/")
		info, err := resolver.DebugResolve(ctx, prefix, true)
		if err != nil || info.RemoteName == "" {
			rawParts = append(rawParts, parts[i])
			continue
		}
		rawParts = append(rawParts, info.RemoteName)
	}
	return "/" + strings.Join(rawParts, "/"), nil
}

func JoinRemotePath(parent, name string) string {
	if parent == "" || parent == "/" {
		return "/" + strings.Trim(name, "/")
	}
	return strings.TrimRight(parent, "/") + "/" + strings.TrimLeft(name, "/")
}

func NewCatCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "cat REMOTE",
		Short:             "Write a remote file to stdout",
		Args:              cliruntime.ExactNamedArgs(rt, "REMOTE"),
		RunE:              runCat(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Bool("raw", false, "read backend bytes without decryption or read-cache transforms")
	return cmd
}

func runCat(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		raw, _ := cmd.Flags().GetBool("raw")
		var rc io.ReadCloser
		if raw {
			reader, ok := fs.(vfs.RawReader)
			if !ok {
				return fmt.Errorf("fs cat --raw: raw read unsupported by filesystem")
			}
			rc, err = reader.ReadRaw(ctx, args[0], 0, 0)
		} else {
			rc, err = fs.Read(ctx, args[0], 0, 0)
		}
		if err != nil {
			return err
		}
		defer rc.Close()
		_, err = io.Copy(cmd.OutOrStdout(), rc)
		return err
	}
}

func NewGetCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get REMOTE LOCAL",
		Short: "Download a remote file or directory",
		Args:  cliruntime.ExactNamedArgs(rt, "REMOTE", "LOCAL"),
		RunE:  runGet(rt),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveDefault
		},
	}
	cmd.Flags().BoolP("force", "f", false, "overwrite an existing local file")
	return cmd
}

func runGet(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		localPath := util.ExpandHome(args[1])
		force, _ := cmd.Flags().GetBool("force")

		entry, err := fs.Stat(ctx, args[0])
		if err != nil {
			return err
		}
		if entry.IsDir {
			targetPath := filepath.Join(localPath, filepath.Base(args[0]))
			total, err := countDirFiles(ctx, fs, args[0])
			if err != nil {
				return err
			}
			if total == 0 {
				fmt.Fprintf(os.Stderr, "  %s: empty directory\n", filepath.Base(args[0]))
				return nil
			}
			bar := newProgressBar(total)
			fmt.Fprintf(os.Stderr, "  downloading %s/\n", filepath.Base(args[0]))
			bar.render()
			if err := getDir(ctx, fs, args[0], targetPath, force, bar); err != nil {
				return err
			}
			bar.done(filepath.Base(args[0]))
			return nil
		}

		return get(ctx, fs, args[0], localPath, force, false)
	}
}

func copyRemoteFile(ctx context.Context, fs vfs.Reader, remotePath string, out io.Writer) error {
	var rc io.ReadCloser
	var err error
	if streamer, ok := fs.(vfs.StreamReader); ok {
		rc, err = streamer.ReadStream(ctx, remotePath)
	} else {
		rc, err = fs.Read(ctx, remotePath, 0, 0)
	}
	if err != nil {
		return err
	}
	defer rc.Close()

	_, err = io.Copy(out, rc)
	return err
}

func get(ctx context.Context, fs vfs.Reader, remotePath, localPath string, force, quiet bool) error {
	if info, err := os.Stat(localPath); err == nil {
		if info.IsDir() {
			return fmt.Errorf("local destination %q is a directory", localPath)
		}
		if !force {
			return fmt.Errorf("local destination %q already exists (use --force to overwrite)", localPath)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	var size int64
	if !quiet {
		entry, err := fs.Stat(ctx, remotePath)
		if err != nil {
			return err
		}
		size = entry.Size
		fmt.Fprintf(os.Stderr, "  downloading %s (%s)\n", filepath.Base(remotePath), util.FormatBytes(size))
	}

	err := util.WriteAtomic(localPath, ".qrypt-download-*", 0o644, force, func(file *os.File) error {
		return copyRemoteFile(ctx, fs, remotePath, file)
	})
	if err != nil {
		return err
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "  downloaded  %s (%s)\n", filepath.Base(remotePath), util.FormatBytes(size))
	}
	return nil
}

func countDirFiles(ctx context.Context, fs vfs.Reader, path string) (int, error) {
	snap, err := sync.SnapshotVFS(ctx, fs, path)
	if err != nil {
		return 0, err
	}
	return snap.FileCount(), nil
}

func getDir(ctx context.Context, fs vfs.Reader, remotePath, localPath string, force bool, bar *progressBar) error {
	if info, err := os.Stat(localPath); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("local destination %q is not a directory", localPath)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if err := os.MkdirAll(localPath, 0o755); err != nil {
		return err
	}

	entries, err := fs.List(ctx, remotePath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		childRemote := remotePath + "/" + entry.Name
		childLocal := filepath.Join(localPath, entry.Name)
		if entry.IsDir {
			if err := getDir(ctx, fs, childRemote, childLocal, force, bar); err != nil {
				return err
			}
		} else if force {
			bar.fileStarted(entry.Name)
			if err := get(ctx, fs, childRemote, childLocal, true, true); err != nil {
				return err
			}
			bar.fileDownloaded(entry.Name)
		} else if _, err := os.Stat(childLocal); os.IsNotExist(err) {
			bar.fileStarted(entry.Name)
			if err := get(ctx, fs, childRemote, childLocal, false, true); err != nil {
				return err
			}
			bar.fileDownloaded(entry.Name)
		} else {
			bar.fileSkipped(entry.Name)
		}
	}
	return nil
}

type progressBar struct {
	total     int
	completed int
	skipped   int
	current   string
}

func newProgressBar(total int) *progressBar {
	return &progressBar{total: total}
}

func (p *progressBar) fileStarted(name string) {
	p.current = name
	p.render()
}

func (p *progressBar) fileDownloaded(name string) {
	p.completed++
	p.current = ""
	p.render()
}

func (p *progressBar) fileSkipped(name string) {
	p.skipped++
	p.current = ""
	p.render()
}

func (p *progressBar) render() {
	const barWidth = 20
	filled := 0
	if p.total > 0 {
		filled = p.completed * barWidth / p.total
	}

	var bar string
	switch {
	case filled >= barWidth:
		bar = strings.Repeat("#", barWidth)
	case filled > 0:
		bar = strings.Repeat("#", filled) + ">" + strings.Repeat("-", barWidth-filled-1)
	default:
		bar = ">" + strings.Repeat("-", barWidth-1)
	}

	status := fmt.Sprintf("[%s] %d/%d", bar, p.completed, p.total)
	if p.skipped > 0 {
		status += fmt.Sprintf(" (%d skipped)", p.skipped)
	}
	if p.current != "" {
		status += "  " + p.current
	}
	fmt.Fprintf(os.Stderr, "\r  %s", status)
}

func (p *progressBar) done(dirName string) {
	downloaded := p.completed
	skipped := p.skipped
	var summary string
	switch {
	case skipped == 0:
		summary = fmt.Sprintf("%d downloaded", downloaded)
	case downloaded == 0:
		summary = fmt.Sprintf("%d skipped", skipped)
	default:
		summary = fmt.Sprintf("%d downloaded, %d skipped", downloaded, skipped)
	}
	fmt.Fprintf(os.Stderr, "\r  [%s] %d/%d  %s\n", strings.Repeat("#", 20), p.completed, p.total, summary)
}
