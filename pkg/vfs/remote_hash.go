package vfs

import (
	"context"
	"io"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// RemoteHash queries the backend for the stored content hash of the file at
// the virtual path, without downloading it. Returns drive.ErrUnsupported when
// the backend does not report hashes.
func (v *VFS) RemoteHash(ctx context.Context, path string) (drive.HashAlgorithm, string, error) {
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return "", "", err
	}
	hasher, ok := v.driver.(drive.RemoteHasher)
	if !ok {
		return "", "", drive.ErrUnsupported
	}
	algorithm, hash, err := hasher.RemoteHash(ctx, entry)
	if err != nil {
		return "", "", err
	}
	return algorithm, hash, nil
}

// RemoteHash is the namespace-level version: it resolves the path to its
// mount and delegates to that mount's backend.
func (n *Namespace) RemoteHash(ctx context.Context, path string) (drive.HashAlgorithm, string, error) {
	mount, rest, _, err := n.resolve(path)
	if err != nil {
		return "", "", err
	}
	if mount == nil {
		return "", "", drive.ErrUnsupported
	}
	return mount.RemoteHash(ctx, rest)
}

// EncryptedHash computes the backend hash the plaintext (from reader) would
// have when encrypted with this mount's cipher and the nonce stored in the
// remote header. Returns drive.ErrUnsupported on non-encrypted mounts.
func (v *VFS) EncryptedHash(ctx context.Context, path string, plain io.Reader, plainSize int64, algorithm drive.HashAlgorithm) (string, error) {
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return "", err
	}
	encrypter, ok := v.driver.(drive.EncryptedHashProvider)
	if !ok {
		return "", drive.ErrUnsupported
	}
	return encrypter.EncryptedHash(ctx, entry, plain, plainSize, algorithm)
}

// EncryptedHash is the namespace-level version.
func (n *Namespace) EncryptedHash(ctx context.Context, path string, plain io.Reader, plainSize int64, algorithm drive.HashAlgorithm) (string, error) {
	mount, rest, _, err := n.resolve(path)
	if err != nil {
		return "", err
	}
	if mount == nil {
		return "", drive.ErrUnsupported
	}
	return mount.EncryptedHash(ctx, rest, plain, plainSize, algorithm)
}
