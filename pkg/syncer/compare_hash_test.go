package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// Embed only the required surface: unexpected reads fail instead of hiding a
// fallback that bypasses the remote hash contract.
type hashReader struct {
	vfs.Reader
	remoteHash func(context.Context, string) (drive.HashAlgorithm, string, error)
}

func (r hashReader) RemoteHash(ctx context.Context, path string) (drive.HashAlgorithm, string, error) {
	return r.remoteHash(ctx, path)
}

func (r hashReader) RemoteHashInfo(ctx context.Context, path string) (drive.RemoteHashInfo, error) {
	algorithm, hash, err := r.remoteHash(ctx, path)
	if err != nil {
		return drive.RemoteHashInfo{}, err
	}
	return drive.RemoteHashInfo{Algorithm: algorithm, Hash: hash, Verified: true, Source: drive.RemoteHashSourceProvider}, nil
}

// declaredHashReader implements only the legacy hash contract, so callers must
// treat its hashes as advisory.
type declaredHashReader struct {
	vfs.Reader
	remoteHash func(context.Context, string) (drive.HashAlgorithm, string, error)
}

func (r declaredHashReader) RemoteHash(ctx context.Context, path string) (drive.HashAlgorithm, string, error) {
	return r.remoteHash(ctx, path)
}

type encryptedHashReader struct {
	hashReader
	encryptedHash func(context.Context, string, io.Reader, int64, drive.HashAlgorithm) (string, error)
}

func (r encryptedHashReader) EncryptedHash(ctx context.Context, path string, plain io.Reader, size int64, algorithm drive.HashAlgorithm) (string, error) {
	return r.encryptedHash(ctx, path, plain, size, algorithm)
}

func TestCompareHashPairVirtual(t *testing.T) {
	backendErr := errors.New("backend unavailable")
	for _, tt := range []struct {
		name      string
		auto      bool
		encrypted bool
		noHasher  bool
		algorithm drive.HashAlgorithm
		hash      string
		failPath  string
		failure   error
		wantEqual bool
		wantErr   string
	}{
		{name: "equal", algorithm: drive.HashSHA1, hash: "abc", wantEqual: true},
		{name: "different", algorithm: drive.HashSHA1, hash: "def"},
		{name: "algorithm mismatch", algorithm: drive.HashMD5, hash: "abc", wantErr: "hash algorithms differ"},
		{name: "auto does not hide algorithm mismatch", auto: true, algorithm: drive.HashMD5, wantErr: "hash algorithms differ"},
		{name: "source failure", failPath: "/a/nested/file", failure: backendErr, wantErr: backendErr.Error()},
		{name: "destination failure", failPath: "/b/nested/file", failure: backendErr, wantErr: backendErr.Error()},
		{name: "auto does not hide backend failure", auto: true, failPath: "/a/nested/file", failure: backendErr, wantErr: backendErr.Error()},
		{name: "unsupported explicit", failPath: "/b/nested/file", failure: drive.ErrUnsupported, wantErr: drive.ErrUnsupported.Error()},
		{name: "unsupported auto", auto: true, failPath: "/b/nested/file", failure: fmt.Errorf("hash: %w", drive.ErrUnsupported), wantEqual: true},
		{name: "missing capability explicit", noHasher: true, wantErr: drive.ErrUnsupported.Error()},
		{name: "missing capability auto", noHasher: true, auto: true, wantEqual: true},
		{name: "encrypted mounts", encrypted: true, wantErr: "encrypted mounts"},
		{name: "auto does not hide encrypted mounts", encrypted: true, auto: true, wantErr: "encrypted mounts"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := Target{Kind: TargetVFS, VFSPath: "/a", Encrypted: tt.encrypted}
			b := Target{Kind: TargetVFS, VFSPath: "/b"}
			var reader vfs.Reader = hashReader{remoteHash: func(ctx context.Context, path string) (drive.HashAlgorithm, string, error) {
				if ctx != t.Context() {
					t.Fatal("context not forwarded")
				}
				if path == tt.failPath {
					return "", "", tt.failure
				}
				switch path {
				case "/a/nested/file":
					return drive.HashSHA1, "abc", nil
				case "/b/nested/file":
					return tt.algorithm, tt.hash, nil
				default:
					t.Fatalf("unexpected hash path %q", path)
					return "", "", nil
				}
			}}
			if tt.noHasher {
				reader = nil
			}
			equal, detail, err := CompareHashPair(t.Context(), reader, a, b, "nested/file", tt.auto)
			assertHashResult(t, equal, err, tt.wantEqual, tt.wantErr)
			if tt.failure != nil && !tt.wantEqual && !errors.Is(err, tt.failure) {
				t.Fatalf("error lost identity: %v", err)
			}
			if tt.wantErr == "" && !tt.noHasher && tt.failure == nil && detail != "abc" {
				t.Fatalf("hash detail = %q, want abc", detail)
			}
		})
	}
}

func TestCompareHashPairOnlyVerifiedHashesProveEquality(t *testing.T) {
	a := Target{Kind: TargetVFS, VFSPath: "/a"}
	b := Target{Kind: TargetVFS, VFSPath: "/b"}
	hash := func(context.Context, string) (drive.HashAlgorithm, string, error) {
		return drive.HashSHA1, "same", nil
	}

	var verified vfs.Reader = hashReader{remoteHash: hash}
	equal, _, err := CompareHashPair(t.Context(), verified, a, b, "file", false)
	if err != nil {
		t.Fatal(err)
	}
	if !equal {
		t.Fatal("verified hashes that match must prove equality")
	}

	var declared vfs.Reader = declaredHashReader{remoteHash: hash}
	equal, _, err = CompareHashPair(t.Context(), declared, a, b, "file", false)
	if err != nil {
		t.Fatal(err)
	}
	if equal {
		t.Fatal("a declared hash must not prove equality")
	}

	local := Target{Kind: TargetLocal, LocalPath: t.TempDir()}
	equal, _, err = CompareHashPair(t.Context(), declared, local, b, "file", false)
	if err != nil {
		t.Fatal(err)
	}
	if equal {
		t.Fatal("a declared remote hash must not prove a local file unchanged")
	}
}

func TestCompareHashPairLocal(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "nested", "file"), "hello")
	for _, algorithm := range []drive.HashAlgorithm{drive.HashMD5, drive.HashSHA1} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%t", algorithm, reverse), func(t *testing.T) {
				local := Target{Kind: TargetLocal, LocalPath: root}
				remote := Target{Kind: TargetVFS, VFSPath: "/remote"}
				want := "5d41402abc4b2a76b9719d911017c592"
				if algorithm == drive.HashSHA1 {
					want = "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d"
				}
				reader := hashReader{remoteHash: func(_ context.Context, path string) (drive.HashAlgorithm, string, error) {
					if path != "/remote/nested/file" {
						t.Fatalf("hash path = %q", path)
					}
					return algorithm, want, nil
				}}
				a, b := local, remote
				if reverse {
					a, b = b, a
				}
				equal, detail, err := CompareHashPair(t.Context(), reader, a, b, "nested/file", false)
				assertHashResult(t, equal, err, true, "")
				if detail != want {
					t.Fatalf("hash detail = %q, want %q", detail, want)
				}
			})
		}
	}
}

func TestCompareHashPairEncryptedLocal(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "file"), "plaintext")
	backendErr := errors.New("encryption failed")
	for _, tt := range []struct {
		name      string
		missing   bool
		noEncrypt bool
		auto      bool
		hash      string
		failure   error
		wantEqual bool
		wantErr   string
	}{
		{name: "equal", hash: "ciphertext-hash", wantEqual: true},
		{name: "different", hash: "different"},
		{name: "missing local file", missing: true, wantErr: "file"},
		{name: "missing capability", noEncrypt: true, wantErr: drive.ErrUnsupported.Error()},
		{name: "auto missing capability", noEncrypt: true, auto: true, wantEqual: true},
		{name: "encryption error", failure: backendErr, wantErr: backendErr.Error()},
		{name: "auto does not hide encryption error", auto: true, failure: backendErr, wantErr: backendErr.Error()},
		{name: "auto unsupported encryption", auto: true, failure: drive.ErrUnsupported, wantEqual: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := hashReader{remoteHash: func(_ context.Context, path string) (drive.HashAlgorithm, string, error) {
				if path != "/remote/file" {
					t.Fatalf("remote hash path = %q", path)
				}
				return drive.HashSHA1, "ciphertext-hash", nil
			}}
			var reader vfs.Reader = encryptedHashReader{hashReader: base, encryptedHash: func(ctx context.Context, path string, plain io.Reader, size int64, algorithm drive.HashAlgorithm) (string, error) {
				data, err := io.ReadAll(plain)
				if err != nil {
					t.Fatal(err)
				}
				if ctx != t.Context() || path != "/remote/file" || string(data) != "plaintext" || size != 9 || algorithm != drive.HashSHA1 {
					t.Fatalf("unexpected encrypted hash input: path=%q data=%q size=%d algorithm=%s", path, data, size, algorithm)
				}
				return tt.hash, tt.failure
			}}
			if tt.noEncrypt {
				reader = base
			}
			local := Target{Kind: TargetLocal, LocalPath: root}
			if tt.missing {
				local.LocalPath = t.TempDir()
			}
			remote := Target{Kind: TargetVFS, VFSPath: "/remote", Encrypted: true}
			equal, _, err := CompareHashPair(t.Context(), reader, remote, local, "file", tt.auto)
			assertHashResult(t, equal, err, tt.wantEqual, tt.wantErr)
			if tt.missing && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing file error = %v", err)
			}
		})
	}
}

func TestCompareHashPairLocalFailures(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "file"), "hello")
	backendErr := errors.New("remote hash unavailable")
	for _, tt := range []struct {
		name      string
		algorithm drive.HashAlgorithm
		failure   error
		missing   bool
		auto      bool
		wantErr   string
	}{
		{name: "different content", algorithm: drive.HashMD5},
		{name: "missing local file", algorithm: drive.HashMD5, missing: true, wantErr: "file"},
		{name: "auto does not hide missing file", algorithm: drive.HashMD5, missing: true, auto: true, wantErr: "file"},
		{name: "unsupported algorithm", algorithm: "unknown", wantErr: "unsupported local hash algorithm"},
		{name: "remote error", failure: backendErr, wantErr: backendErr.Error()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader := hashReader{remoteHash: func(context.Context, string) (drive.HashAlgorithm, string, error) {
				return tt.algorithm, "not-the-local-hash", tt.failure
			}}
			local := Target{Kind: TargetLocal, LocalPath: root}
			if tt.missing {
				local.LocalPath = t.TempDir()
			}
			remote := Target{Kind: TargetVFS, VFSPath: "/remote"}
			equal, _, err := CompareHashPair(t.Context(), reader, local, remote, "file", tt.auto)
			assertHashResult(t, equal, err, false, tt.wantErr)
			if tt.missing && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing file error = %v", err)
			}
			if tt.failure != nil && !errors.Is(err, tt.failure) {
				t.Fatalf("error lost identity: %v", err)
			}
		})
	}
}

func assertHashResult(t *testing.T, equal bool, err error, wantEqual bool, wantErr string) {
	t.Helper()
	if equal != wantEqual {
		t.Errorf("equal = %t, want %t", equal, wantEqual)
	}
	if wantErr == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	} else if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("error = %v, want containing %q", err, wantErr)
	}
}
