package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
)

func newFsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fs",
		Short: "Run one-shot filesystem operations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newFsListCmd())
	cmd.AddCommand(newFsCatCmd())
	cmd.AddCommand(newFsPutCmd())
	cmd.AddCommand(newFsPendingCmd())
	return cmd
}

func newFsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [path]",
		Short: "List a directory",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runList,
	}
}

func runList(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	path := "/"
	if len(args) > 0 {
		path = args[0]
	}
	fs, cleanup, err := buildFileSystem(ctx, cmd, driverName, root, "", configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding)
	if err != nil {
		return err
	}
	defer cleanup()
	fs.Start(ctx)

	entries, err := fs.List(ctx, path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		kind := "file"
		if entry.IsDir {
			kind = "dir "
		}
		fmt.Printf("%s %10d %s\n", kind, entry.Size, entry.Name)
	}
	return nil
}

func newFsCatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cat REMOTE",
		Short: "Print a remote file",
		Args:  cobra.ExactArgs(1),
		RunE:  runCat,
	}
}

func runCat(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	fs, cleanup, err := buildFileSystem(ctx, cmd, driverName, root, "", configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding)
	if err != nil {
		return err
	}
	defer cleanup()
	fs.Start(ctx)

	rc, err := fs.Read(ctx, args[0], 0, 0)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(os.Stdout, rc)
	return err
}

func newFsPutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "put LOCAL REMOTE",
		Short: "Upload a local file",
		Args:  cobra.ExactArgs(2),
		RunE:  runPut,
	}
}

func runPut(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	fs, cleanup, err := buildFileSystem(ctx, cmd, driverName, root, "", configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding)
	if err != nil {
		return err
	}
	defer cleanup()
	fs.Start(ctx)

	return put(ctx, fs, args[0], args[1])
}

func newFsPendingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "Show pending uploads",
		Args:  cobra.NoArgs,
		RunE:  runPending,
	}
	cmd.Flags().Bool("verbose", false, "show detailed output")
	return cmd
}

func runPending(cmd *cobra.Command, args []string) error {
	if err := requireConfig(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	fs, cleanup, err := buildFileSystem(ctx, cmd, driverName, root, "", configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding)
	if err != nil {
		return err
	}
	defer cleanup()
	fs.Start(ctx)

	verbose, _ := cmd.Flags().GetBool("verbose")
	if verbose {
		printPendingVerbose(os.Stdout, fs.Pending())
		return nil
	}
	for _, pending := range fs.Pending() {
		fmt.Printf("%s %d %s\n", pending.Path, pending.Size, pending.LocalPath)
	}
	return nil
}
