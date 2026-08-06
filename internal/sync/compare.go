package sync

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// CompareOptions configures the comparison. Hash is called for entries whose
// size and mtime match, when the caller wants content verification.
type CompareOptions struct {
	AsHash   bool
	AutoHash bool
	SkipSize bool
	Hash     func(ctx context.Context, rel string) (bool, string, error)
}

// ParseCompareMode maps the --compare flag to comparison settings.
func ParseCompareMode(mode string) (skipSize, forceHash bool, err error) {
	switch mode {
	case "size-mtime":
		return false, false, nil
	case "mtime-only":
		return true, false, nil
	case "hash":
		return false, true, nil
	default:
		return false, false, fmt.Errorf("invalid --compare mode %q (want size-mtime, mtime-only, or hash)", mode)
	}
}

// Difference is one path where the two snapshots disagree.
type Difference struct {
	Path   string
	Reason string
	IsDir  bool
	// A and B carry human-readable detail for the check command (sizes,
	// mtimes or hash values when available).
	A string
	B string
}

// CompareTrees produces the ordered list of differences between source and
// destination snapshots, path-sorted for deterministic plans.
func CompareTrees(ctx context.Context, source, destination Snapshot, opts CompareOptions) ([]Difference, error) {
	var diffs []Difference
	paths := sortedPaths(source, destination)
	for _, p := range paths {
		entryA, hasA := source[p]
		entryB, hasB := destination[p]
		switch {
		case !hasB:
			diffs = append(diffs, Difference{Path: p, IsDir: entryA.IsDir, Reason: "missing_in_b"})
		case !hasA:
			diffs = append(diffs, Difference{Path: p, IsDir: entryB.IsDir, Reason: "extra_in_b"})
		case entryA.IsDir != entryB.IsDir:
			diffs = append(diffs, Difference{Path: p, IsDir: entryA.IsDir, Reason: "type", A: EntryKind(entryA), B: EntryKind(entryB)})
		case entryA.IsDir:
			// Directories differ only by type; children are compared below.
		case opts.SkipSize && entryA.ModTime != entryB.ModTime:
			diffs = append(diffs, Difference{Path: p, IsDir: false, Reason: "mtime", A: fmt.Sprintf("%d", entryA.ModTime), B: fmt.Sprintf("%d", entryB.ModTime)})
		case !opts.SkipSize && entryA.Size != entryB.Size:
			diffs = append(diffs, Difference{Path: p, IsDir: false, Reason: "size", A: fmt.Sprintf("%d", entryA.Size), B: fmt.Sprintf("%d", entryB.Size)})
		case entryA.ModTime != entryB.ModTime:
			diffs = append(diffs, Difference{Path: p, IsDir: false, Reason: "mtime", A: fmt.Sprintf("%d", entryA.ModTime), B: fmt.Sprintf("%d", entryB.ModTime)})
		case opts.AsHash:
			if opts.Hash == nil {
				continue
			}
			equal, detail, err := opts.Hash(ctx, p)
			if err != nil {
				return nil, err
			}
			if !equal {
				diffs = append(diffs, Difference{Path: p, IsDir: false, Reason: "hash", A: detail})
			}
		}
	}
	return diffs, nil
}

// CompareHashPair compares the content hash of one path on both sides.
// autoHash controls degradation: with autoHash, a backend that cannot
// provide hashes counts as a match (size already compared); without it,
// missing hash support fails loudly (explicit --hash).
func CompareHashPair(ctx context.Context, fs vfs.FileSystem, a, b Target, rel string, autoHash bool) (bool, string, error) {
	degrade := func(err error) (bool, string, error) {
		if autoHash && errors.Is(err, drive.ErrUnsupported) {
			return true, "", nil // backend cannot verify content; assume match (size compared)
		}
		return false, "", err
	}
	// Case 1: both sides virtual.
	if a.Kind == TargetVFS && b.Kind == TargetVFS {
		if a.Encrypted || b.Encrypted {
			return degrade(fmt.Errorf("hash comparison between encrypted mounts is not supported"))
		}
		hasher, ok := fs.(vfs.HashProvider)
		if !ok {
			return degrade(drive.ErrUnsupported)
		}
		algA, hashA, err := hasher.RemoteHash(ctx, JoinVFS(a.VFSPath, rel))
		if err != nil {
			return degrade(err)
		}
		algB, hashB, err := hasher.RemoteHash(ctx, JoinVFS(b.VFSPath, rel))
		if err != nil {
			return degrade(err)
		}
		if algA != algB {
			return degrade(fmt.Errorf("hash algorithms differ between sides: %s vs %s", algA, algB))
		}
		return hashA == hashB, hashA, nil
	}
	// Case 2: one side is local. Local files hash as-is; the virtual side
	// re-encrypts the local plaintext when the mount is encrypted so the
	// ciphertext hashes are comparable.
	virtual, local := a, b
	if a.Kind != TargetVFS {
		virtual, local = b, a
	}
	openLocal := func() (*os.File, int64, error) {
		f, err := os.Open(filepath.Join(local.LocalPath, filepath.FromSlash(rel)))
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
	hasher, ok := fs.(vfs.HashProvider)
	if !ok {
		return degrade(drive.ErrUnsupported)
	}
	algorithm, remote, err := hasher.RemoteHash(ctx, JoinVFS(virtual.VFSPath, rel))
	if err != nil {
		return degrade(err)
	}
	if virtual.Encrypted {
		// The remote hash covers encrypted bytes; re-encrypt the local
		// plaintext with the remote nonce and compare ciphertext hashes.
		plain, size, err := openLocal()
		if err != nil {
			return false, "", err
		}
		defer plain.Close()
		encrypter, ok := fs.(vfs.EncryptedHashProvider)
		if !ok {
			return degrade(drive.ErrUnsupported)
		}
		recomputed, err := encrypter.EncryptedHash(ctx, JoinVFS(virtual.VFSPath, rel), plain, size, algorithm)
		if err != nil {
			return degrade(err)
		}
		return recomputed == remote, remote, nil
	}
	// Non-encrypted virtual side: remote hash is the plaintext hash; compute
	// the same algorithm on the local file.
	localHash, err := LocalFileHash(filepath.Join(local.LocalPath, filepath.FromSlash(rel)), algorithm)
	if err != nil {
		return degrade(err)
	}
	return localHash == remote, remote, nil
}

// localFileHash computes a local file's content hash with the given algorithm.
func LocalFileHash(path string, algorithm drive.HashAlgorithm) (string, error) {
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

func sortedPaths(a, b Snapshot) []string {
	seen := map[string]struct{}{}
	for p := range a {
		seen[p] = struct{}{}
	}
	for p := range b {
		seen[p] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// EntryKind renders a snapshot entry's type for difference detail output.
func EntryKind(entry Entry) string {
	if entry.IsDir {
		return "dir"
	}
	return fmt.Sprintf("file(%d)", entry.Size)
}
