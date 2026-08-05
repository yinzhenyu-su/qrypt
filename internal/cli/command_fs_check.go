package cli

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func newFsCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check A B",
		Short: "Check that two trees contain the same files",
		Long: `Check that the trees at A and B contain the same files.

A and B can be virtual paths under a configured mount (/MOUNT/dir) or local
paths. Encryption is handled transparently: the mount's [mounts.encryption]
is applied when comparing, so a local plaintext directory can be checked
against an encrypted cloud mount.

Differences are reported relative to the argument order: missing_in_b means
a file exists on A but not B; extra_in_b means it exists on B but not A.

Exit code: 0 when identical, 4 when differences are found, 1 on errors.`,
		Args:              exactNamedArgs("A", "B"),
		RunE:              runCheck,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("hash", false, "also compare content hashes (needs backend hash support)")
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

type checkTargetKind int

const (
	targetLocal checkTargetKind = iota
	targetVFS
)

type checkTarget struct {
	kind      checkTargetKind
	raw       string
	vfsPath   string
	localPath string
	mountName string
	encrypted bool
}

// resolveCheckTarget classifies an argument as a virtual path (first segment
// is a configured mount) or a local path.
func resolveCheckTarget(cfg *config.Config, arg string) checkTarget {
	if !strings.HasPrefix(arg, "/") {
		return checkTarget{kind: targetLocal, raw: arg, localPath: arg}
	}
	first := strings.Split(strings.TrimPrefix(arg, "/"), "/")[0]
	for _, mount := range cfg.Mounts {
		if mount.Name == first {
			return checkTarget{
				kind:      targetVFS,
				raw:       arg,
				vfsPath:   arg,
				mountName: mount.Name,
				encrypted: cfg.EncryptionFor(mount.Name).Password != "",
			}
		}
	}
	return checkTarget{kind: targetLocal, raw: arg, localPath: arg}
}

func runCheck(cmd *cobra.Command, args []string) error {
	state, err := commandConfig(cmd)
	if err != nil {
		return err
	}
	if state.cfg == nil {
		return configNotFoundError()
	}
	targetA := resolveCheckTarget(state.cfg, args[0])
	targetB := resolveCheckTarget(state.cfg, args[1])
	if targetA.kind == targetLocal && targetB.kind == targetLocal {
		return commandUsageError(cmd, "at least one side must be a virtual path (/MOUNT/...): got %q and %q", args[0], args[1])
	}

	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	asHash, _ := cmd.Flags().GetBool("hash")
	snapA, err := snapshotTarget(ctx, fs, targetA)
	if err != nil {
		return err
	}
	snapB, err := snapshotTarget(ctx, fs, targetB)
	if err != nil {
		return err
	}
	opts := treeCompareOptions{AsHash: asHash}
	if asHash {
		opts.Hash = func(ctx context.Context, rel string) (bool, string, error) {
			return compareVFSHashPair(ctx, fs, targetA, targetB, rel, false)
		}
	}
	differences, err := compareTrees(ctx, snapA, snapB, opts)
	if err != nil {
		return err
	}
	filesChecked := snapA.fileCount()

	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if err := writePrettyJSON(cmd.OutOrStdout(), struct {
			OK           bool             `json:"ok"`
			FilesChecked int              `json:"files_checked"`
			Differences  []treeDifference `json:"differences"`
		}{
			OK:           len(differences) == 0,
			FilesChecked: filesChecked,
			Differences:  differences,
		}); err != nil {
			return err
		}
	} else if len(differences) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "ok: %d files match\n", filesChecked)
	} else {
		for _, d := range differences {
			fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s", d.Reason, d.Path)
			if d.A != "" || d.B != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " (%s vs %s)", d.A, d.B)
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}
	}
	if len(differences) == 0 {
		return nil
	}
	return &ExitError{Code: ExitMismatch, Err: fmt.Errorf("check found %d difference(s)", len(differences))}
}

// compareVFSHashPair hashes one file on both sides (argument order a, b) and
// reports whether they match. The encrypted-mount case re-encrypts the local
// plaintext with the remote nonce so no file body is downloaded.
//
// autoHash degrades to "match" when the backends cannot provide hashes
// (unsupported backend, missing hash, algorithm mismatch) instead of
// erroring; fs check passes false (explicit --hash must fail loudly), fs
// sync passes true for its default comparison.
func compareVFSHashPair(ctx context.Context, fs vfs.FileSystem, a, b checkTarget, rel string, autoHash bool) (bool, string, error) {
	degrade := func(err error) (bool, string, error) {
		if autoHash {
			return true, "", nil // cannot verify content; assume match (size compared)
		}
		return false, "", err
	}
	// Case 1: both sides virtual.
	if a.kind == targetVFS && b.kind == targetVFS {
		if a.encrypted || b.encrypted {
			return degrade(fmt.Errorf("hash comparison between encrypted mounts is not supported"))
		}
		hasher, ok := fs.(interface {
			RemoteHash(ctx context.Context, path string) (drive.HashAlgorithm, string, error)
		})
		if !ok {
			return degrade(drive.ErrUnsupported)
		}
		algA, hashA, err := hasher.RemoteHash(ctx, joinVFS(a.vfsPath, rel))
		if err != nil {
			return degrade(err)
		}
		algB, hashB, err := hasher.RemoteHash(ctx, joinVFS(b.vfsPath, rel))
		if err != nil {
			return degrade(err)
		}
		if algA != algB {
			return degrade(fmt.Errorf("hash algorithms differ between sides: %s vs %s", algA, algB))
		}
		return hashA == hashB, hashA, nil
	}

	// Exactly one virtual side; the other is local.
	virtual, local := a, b
	if a.kind == targetLocal {
		virtual, local = b, a
	}
	openLocal := func() (io.ReadCloser, int64, error) {
		f, err := os.Open(filepath.Join(local.localPath, filepath.FromSlash(rel)))
		if err != nil {
			return nil, 0, err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, err
		}
		return f, info.Size(), nil
	}

	hasher, ok := fs.(interface {
		RemoteHash(ctx context.Context, path string) (drive.HashAlgorithm, string, error)
	})
	if !ok {
		return degrade(drive.ErrUnsupported)
	}
	algorithm, remote, err := hasher.RemoteHash(ctx, joinVFS(virtual.vfsPath, rel))
	if err != nil {
		return degrade(err)
	}

	if virtual.encrypted {
		// The remote hash covers encrypted bytes; re-encrypt the local
		// plaintext with the remote nonce and compare ciphertext hashes.
		plain, size, err := openLocal()
		if err != nil {
			return false, "", err
		}
		defer plain.Close()
		encrypter, ok := fs.(interface {
			EncryptedHash(ctx context.Context, path string, plain io.Reader, plainSize int64, algorithm drive.HashAlgorithm) (string, error)
		})
		if !ok {
			return degrade(drive.ErrUnsupported)
		}
		recomputed, err := encrypter.EncryptedHash(ctx, joinVFS(virtual.vfsPath, rel), plain, size, algorithm)
		if err != nil {
			return degrade(err)
		}
		return recomputed == remote, remote, nil
	}

	// Non-encrypted virtual side: remote hash is the plaintext hash; compute
	// the same algorithm on the local file.
	localHash, err := localFileHash(filepath.Join(local.localPath, filepath.FromSlash(rel)), algorithm)
	if err != nil {
		return degrade(err)
	}
	return localHash == remote, remote, nil
}

func joinVFS(base, rel string) string {
	return pathpkg.Join(base, rel)
} // localFileHash computes a local file's content hash with the given algorithm.
func localFileHash(path string, algorithm drive.HashAlgorithm) (string, error) {
	var h hash.Hash
	switch algorithm {
	case drive.HashSHA1:
		h = sha1.New()
	case drive.HashMD5:
		h = md5.New()
	default:
		return "", fmt.Errorf("unsupported local hash algorithm %q", algorithm)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
