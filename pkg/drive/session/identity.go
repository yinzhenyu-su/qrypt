// Package session defines the qrypt upload-session contract: a tiny local
// binding from a content-addressed upload identity to a provider-side upload
// reference (S3 UploadID, OneDrive upload URL, SFTP staging path).
//
// The binding is all a driver ever persists. Upload progress is never stored
// locally: drivers reconstruct it from the provider (ListParts, stat,
// nextExpectedRanges), so the local file stays small and per-part work costs
// zero disk I/O. Bindings are written before provider work starts
// ("reserve, then create"), so a crash at any point leaves either a resumable
// upload or a reclaimable one — never an untracked orphan.
package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// DefaultMaxAge bounds how long an unused binding survives before the driver's
// expiry pass reclaims the provider-side upload.
const DefaultMaxAge = 7 * 24 * time.Hour

// Identity binds one upload to its content: changing any input changes Key,
// so completed parts can never be reused for different content.
type Identity struct {
	ParentID    string
	Name        string
	Size        int64
	Fingerprint string // hex content fingerprint; the algorithm is the provider's
	// mandated one (SHA-256, SHA-1, MD5, ...) — empty when the caller
	// did not provide one
}

// Resumable reports whether the identity is strong enough to resume an
// existing provider upload. Without a content fingerprint a same-size content
// change could silently mix old and new parts, so resume is refused.
func (id Identity) Resumable() bool {
	return id.Fingerprint != ""
}

// Key returns the content-addressed key of the identity.
func (id Identity) Key() string {
	h := sha256.New()
	for _, part := range []string{id.ParentID, id.Name, strconv.FormatInt(id.Size, 10), id.Fingerprint} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ContentSHA256Hex returns the source's SHA-256 as hex. It prefers
// caller-provided hash metadata (drive.SourceHash) and only streams the source
// when that metadata is unavailable, keeping resumable uploads cheap for
// callers that already computed hashes.
func ContentSHA256Hex(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (string, error) {
	if sum, ok := drive.SourceHash(source, drive.HashSHA256); ok {
		if len(sum) != sha256.Size {
			return "", fmt.Errorf("session: source SHA-256 metadata has %d bytes, want %d", len(sum), sha256.Size)
		}
		return hex.EncodeToString(sum), nil
	}
	file, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("session: hash source open: %w", err)
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(hasher, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", fmt.Errorf("session: hash source: %w", copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("session: close hash source: %w", closeErr)
	}
	if written != size {
		return "", fmt.Errorf("session: source size mismatch: hashed %d, expected %d", written, size)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
