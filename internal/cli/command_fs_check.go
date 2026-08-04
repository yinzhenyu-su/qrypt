package cli

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// checkDifference records one mismatch between the two checked trees.
type checkDifference struct {
	Path   string `json:"path"`
	Reason string `json:"reason"` // missing_in_b, extra_in_b, size, mtime, hash
	A      string `json:"a,omitempty"`
	B      string `json:"b,omitempty"`
}

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
	var differences []checkDifference
	filesChecked := 0
	if targetA.kind == targetVFS && targetB.kind == targetVFS {
		differences, filesChecked, err = checkTwoVFSTrees(ctx, fs, targetA, targetB, asHash)
	} else {
		differences, filesChecked, err = checkVFSAgainstLocal(ctx, fs, targetA, targetB, asHash)
	}
	if err != nil {
		return err
	}

	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if err := writePrettyJSON(cmd.OutOrStdout(), struct {
			OK           bool              `json:"ok"`
			FilesChecked int               `json:"files_checked"`
			Differences  []checkDifference `json:"differences"`
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

// checkTwoVFSTrees compares two virtual trees. Missing/extra are relative to
// the argument order (a is the first argument).
func checkTwoVFSTrees(ctx context.Context, fs vfs.FileSystem, a, b checkTarget, asHash bool) ([]checkDifference, int, error) {
	treeA, err := walkVFSTree(ctx, fs, a.vfsPath)
	if err != nil {
		return nil, 0, fmt.Errorf("walk %s: %w", a.raw, err)
	}
	treeB, err := walkVFSTree(ctx, fs, b.vfsPath)
	if err != nil {
		return nil, 0, fmt.Errorf("walk %s: %w", b.raw, err)
	}
	var diffs []checkDifference
	for rel, entryA := range treeA {
		entryB, ok := treeB[rel]
		if !ok {
			diffs = append(diffs, checkDifference{Path: rel, Reason: "missing_in_b"})
			continue
		}
		if entryA.Size != entryB.Size {
			diffs = append(diffs, checkDifference{Path: rel, Reason: "size", A: fmt.Sprintf("%d", entryA.Size), B: fmt.Sprintf("%d", entryB.Size)})
			continue
		}
		if entryA.ModTime.Unix() != entryB.ModTime.Unix() {
			diffs = append(diffs, checkDifference{Path: rel, Reason: "mtime", A: entryA.ModTime.String(), B: entryB.ModTime.String()})
			continue
		}
		if asHash {
			matched, detail, err := compareVFSHashPair(ctx, fs, a, b, rel)
			if err != nil {
				return diffs, len(treeA), err
			}
			if !matched {
				diffs = append(diffs, checkDifference{Path: rel, Reason: "hash", A: detail})
			}
		}
	}
	for rel := range treeB {
		if _, ok := treeA[rel]; !ok {
			diffs = append(diffs, checkDifference{Path: rel, Reason: "extra_in_b"})
		}
	}
	return diffs, len(treeA), nil
}

// checkVFSAgainstLocal compares a virtual tree against a local directory.
// a and b keep the argument order; exactly one of them is a virtual target.
func checkVFSAgainstLocal(ctx context.Context, fs vfs.FileSystem, a, b checkTarget, asHash bool) ([]checkDifference, int, error) {
	virtual, local := a, b
	if a.kind == targetLocal {
		virtual, local = b, a
	}
	treeV, err := walkVFSTree(ctx, fs, virtual.vfsPath)
	if err != nil {
		return nil, 0, fmt.Errorf("walk %s: %w", virtual.raw, err)
	}
	treeL, err := walkLocalTree(local.localPath)
	if err != nil {
		return nil, 0, fmt.Errorf("walk %s: %w", local.raw, err)
	}
	// virtualIsA tells whether the virtual tree is the first argument, which
	// decides the missing/extra direction.
	virtualIsA := virtual.raw == a.raw

	var diffs []checkDifference

	for rel, entryV := range treeV {
		infoL, ok := treeL[rel]
		if !ok {
			if virtualIsA {
				diffs = append(diffs, checkDifference{Path: rel, Reason: "missing_in_b"})
			} else {
				diffs = append(diffs, checkDifference{Path: rel, Reason: "extra_in_b"})
			}
			continue
		}
		if entryV.Size != infoL.size {
			diffs = append(diffs, checkDifference{Path: rel, Reason: "size", A: fmt.Sprintf("%d", entryV.Size), B: fmt.Sprintf("%d", infoL.size)})
			continue
		}
		if entryV.ModTime.Unix() != infoL.modTime {
			diffs = append(diffs, checkDifference{Path: rel, Reason: "mtime", A: entryV.ModTime.String(), B: time.Unix(infoL.modTime, 0).String()})
			continue
		}
		if asHash {
			matched, detail, err := compareVFSHashPair(ctx, fs, a, b, rel)
			if err != nil {
				return diffs, len(treeV), err
			}
			if !matched {
				diffs = append(diffs, checkDifference{Path: rel, Reason: "hash", A: detail})
			}
		}
	}
	for rel := range treeL {
		if _, ok := treeV[rel]; ok {
			continue
		}
		if virtualIsA {
			diffs = append(diffs, checkDifference{Path: rel, Reason: "extra_in_b"})
		} else {
			diffs = append(diffs, checkDifference{Path: rel, Reason: "missing_in_b"})
		}
	}
	return diffs, len(treeV), nil
}

// compareVFSHashPair hashes one file on both sides (argument order a, b) and
// reports whether they match. The encrypted-mount case re-encrypts the local
// plaintext with the remote nonce so no file body is downloaded.
func compareVFSHashPair(ctx context.Context, fs vfs.FileSystem, a, b checkTarget, rel string) (bool, string, error) {
	// Case 1: both sides virtual.
	if a.kind == targetVFS && b.kind == targetVFS {
		if a.encrypted || b.encrypted {
			return false, "", fmt.Errorf("hash comparison between encrypted mounts is not supported")
		}
		hasher, ok := fs.(interface {
			RemoteHash(ctx context.Context, path string) (drive.HashAlgorithm, string, error)
		})
		if !ok {
			return false, "", drive.ErrUnsupported
		}
		algA, hashA, err := hasher.RemoteHash(ctx, joinVFS(a.vfsPath, rel))
		if err != nil {
			return false, "", err
		}
		algB, hashB, err := hasher.RemoteHash(ctx, joinVFS(b.vfsPath, rel))
		if err != nil {
			return false, "", err
		}
		if algA != algB {
			return false, "", fmt.Errorf("hash algorithms differ between sides: %s vs %s", algA, algB)
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
		return false, "", drive.ErrUnsupported
	}
	algorithm, remote, err := hasher.RemoteHash(ctx, joinVFS(virtual.vfsPath, rel))
	if err != nil {
		return false, "", err
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
			return false, "", drive.ErrUnsupported
		}
		recomputed, err := encrypter.EncryptedHash(ctx, joinVFS(virtual.vfsPath, rel), plain, size, algorithm)
		if err != nil {
			return false, "", err
		}
		return recomputed == remote, remote, nil
	}

	// Non-encrypted virtual side: remote hash is the plaintext hash; compute
	// the same algorithm on the local file.
	localHash, err := localFileHash(filepath.Join(local.localPath, filepath.FromSlash(rel)), algorithm)
	if err != nil {
		return false, "", err
	}
	return localHash == remote, remote, nil
}

func joinVFS(base, rel string) string {
	return pathpkg.Join(base, rel)
}

// walkVFSTree lists every file under root, keyed by slash-separated relative
// path.
func walkVFSTree(ctx context.Context, fs vfs.FileSystem, root string) (map[string]drive.Entry, error) {
	result := map[string]drive.Entry{}
	var walk func(dir, prefix string) error
	walk = func(dir, prefix string) error {
		entries, err := fs.List(ctx, dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			rel := pathpkg.Join(prefix, entry.Name)
			if entry.IsDir {
				if err := walk(pathpkg.Join(dir, entry.Name), rel); err != nil {
					return err
				}
				continue
			}
			result[rel] = entry
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	return result, nil
}

type localTreeFile struct {
	size    int64
	modTime int64
}

// walkLocalTree lists every file under root, keyed by slash-separated
// relative path.
func walkLocalTree(root string) (map[string]localTreeFile, error) {
	result := map[string]localTreeFile{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(rel)] = localTreeFile{size: info.Size(), modTime: info.ModTime().Unix()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// localFileHash computes a local file's content hash with the given algorithm.
func localFileHash(path string, algorithm drive.HashAlgorithm) (string, error) {
	if algorithm != drive.HashSHA1 {
		return "", fmt.Errorf("unsupported local hash algorithm %q", algorithm)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
