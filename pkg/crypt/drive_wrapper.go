package crypt

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Driver wraps a raw backend with rclone-compatible name and content crypto.
type Driver struct {
	raw          drive.Driver
	cp           Cipher
	contentDedup bool
	nonceCache   sync.Map
	hashCache    sync.Map
}

type DriverOptions struct {
	ContentDedup bool
}

// EntryExtra preserves raw backend metadata for entries whose public name and
// size have been transformed by the crypt driver.
type EntryExtra struct {
	RemoteName string
	RawExtra   any
}

func (e EntryExtra) EntryRemoteName() string {
	return e.RemoteName
}

// RemoteName returns the backend object name captured before filename
// decryption. Non-crypt entries fall back to their public name.
func RemoteName(entry drive.Entry) (string, bool) {
	return drive.EntryRemoteName(entry)
}

func NewDriver(raw drive.Driver, cp Cipher, opts DriverOptions) *Driver {
	return &Driver{raw: raw, cp: cp, contentDedup: opts.ContentDedup}
}

func (d *Driver) Encrypted() bool {
	return true
}

func (d *Driver) Capabilities() []drive.Capability {
	caps := []drive.Capability{
		drive.CapabilityForeignEntries,
		drive.CapabilityRemoteNameResolver,
	}
	if drive.HasCapability(d.raw, drive.CapabilityWriter) {
		caps = append(caps, drive.CapabilityWriter)
	}
	if drive.HasCapability(d.raw, drive.CapabilitySourceUploader) {
		caps = append(caps, drive.CapabilitySourceUploader)
	}
	if drive.HasCapability(d.raw, drive.CapabilityResumableUploader) {
		caps = append(caps, drive.CapabilityResumableUploader)
	}
	if drive.HasCapability(d.raw, drive.CapabilitySpace) {
		caps = append(caps, drive.CapabilitySpace)
	}
	return caps
}

func (d *Driver) Init(ctx context.Context) error { return d.raw.Init(ctx) }

func (d *Driver) Drop(ctx context.Context) error { return d.raw.Drop(ctx) }

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	return d.raw.Space(ctx)
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	snapshot, err := d.raw.DebugSnapshot(ctx)
	if err != nil {
		return drive.DebugSnapshot{}, err
	}
	if snapshot.Extra == nil {
		snapshot.Extra = map[string]any{}
	}
	snapshot.Extra["crypt"] = true
	snapshot.Extra["content_dedup"] = d.contentDedup
	return snapshot, nil
}

func (d *Driver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.raw.Metrics(ctx, since)
}

func (d *Driver) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	return drive.RemoteNameInfo{
		PlainName:  plainName,
		RemoteName: d.cp.EncryptSegment(plainName),
	}, nil
}

func (d *Driver) ForeignEntries(ctx context.Context, parentID string) ([]drive.ForeignEntry, error) {
	entries, err := d.raw.List(ctx, parentID)
	if err != nil {
		return nil, err
	}
	foreign := make([]drive.ForeignEntry, 0)
	for _, entry := range entries {
		name, err := d.cp.DecryptSegment(entry.Name)
		if err != nil {
			foreign = append(foreign, drive.ForeignEntry{
				ID:         entry.ID,
				ParentID:   entry.ParentID,
				RemoteName: entry.Name,
				IsDir:      entry.IsDir,
				Size:       entry.Size,
				Reason:     "filename_decrypt_failed",
			})
			continue
		}
		if !validPlainName(name) {
			foreign = append(foreign, drive.ForeignEntry{
				ID:         entry.ID,
				ParentID:   entry.ParentID,
				RemoteName: entry.Name,
				IsDir:      entry.IsDir,
				Size:       entry.Size,
				Reason:     "invalid_plain_filename",
			})
		}
	}
	return foreign, nil
}

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	entries, err := d.raw.List(ctx, parentID)
	if err != nil {
		return nil, err
	}
	out := entries[:0]
	for i := range entries {
		entry := entries[i]
		rawName := entry.Name
		entry.Extra = EntryExtra{RemoteName: rawName, RawExtra: entry.Extra}
		name, err := d.cp.DecryptSegment(entry.Name)
		if err != nil || !validPlainName(name) {
			continue
		}
		entry.Name = strings.TrimSpace(name)
		if !entry.IsDir {
			if size, err := d.cp.DecryptedSize(entry.Size); err == nil {
				entry.Size = size
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func validPlainName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" &&
		name != "." &&
		name != ".." &&
		utf8.ValidString(name) &&
		!strings.ContainsAny(name, "/\x00")
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || size < 0 {
		return nil, errors.New("crypt: read offset and size must be non-negative")
	}
	if offset == 0 && size == 0 {
		return d.readAll(ctx, entry)
	}
	nonce, err := d.fileNonce(ctx, entry)
	if err != nil {
		return nil, err
	}
	blockIndex := offset / BlockDataSize
	blockOffset := offset % BlockDataSize
	encOffset := int64(FileHeaderSize) + blockIndex*BlockSize
	encSize := int64(BlockSize)
	if size > 0 {
		plainNeeded := blockOffset + size
		blocks := (plainNeeded + BlockDataSize - 1) / BlockDataSize
		encSize = blocks * BlockSize
	}
	if entry.Size > 0 {
		encTotal := d.cp.EncryptedSize(entry.Size)
		if encOffset >= encTotal {
			return io.NopCloser(strings.NewReader("")), nil
		}
		if encOffset+encSize > encTotal {
			encSize = encTotal - encOffset
		}
	}
	rc, err := d.raw.Read(ctx, entry, encOffset, encSize)
	if err != nil {
		return nil, err
	}
	reader := io.Reader(NewDecryptingReaderAt(rc, d.cp, nonce, uint64(blockIndex)))
	if blockOffset > 0 {
		reader = &discardPrefixReader{reader: reader, discard: blockOffset, closer: rc}
	}
	if size > 0 {
		reader = io.LimitReader(reader, size)
	}
	return struct {
		io.Reader
		io.Closer
	}{Reader: reader, Closer: rc}, nil
}

func (d *Driver) readAll(ctx context.Context, entry drive.Entry) (io.ReadCloser, error) {
	rc, err := d.raw.Read(ctx, entry, 0, 0)
	if err != nil {
		return nil, err
	}
	header := make([]byte, FileHeaderSize)
	if _, err := io.ReadFull(rc, header); err != nil {
		rc.Close()
		return nil, fmt.Errorf("crypt: read header: %w", err)
	}
	if string(header[:FileMagicSize]) != FileMagic {
		rc.Close()
		return nil, errors.New("crypt: invalid rclone file header")
	}
	var nonce [FileNonceSize]byte
	copy(nonce[:], header[FileMagicSize:])
	d.nonceCache.Store(entry.ID, nonce)
	reader := io.Reader(NewDecryptingReader(rc, d.cp, nonce))
	return struct {
		io.Reader
		io.Closer
	}{Reader: reader, Closer: rc}, nil
}

func (d *Driver) fileNonce(ctx context.Context, entry drive.Entry) ([FileNonceSize]byte, error) {
	if cached, ok := d.nonceCache.Load(entry.ID); ok {
		return cached.([FileNonceSize]byte), nil
	}
	rc, err := d.raw.Read(ctx, entry, 0, int64(FileHeaderSize))
	if err != nil {
		return [FileNonceSize]byte{}, err
	}
	defer rc.Close()
	header := make([]byte, FileHeaderSize)
	if _, err := io.ReadFull(rc, header); err != nil {
		return [FileNonceSize]byte{}, fmt.Errorf("crypt: read header: %w", err)
	}
	if string(header[:FileMagicSize]) != FileMagic {
		return [FileNonceSize]byte{}, errors.New("crypt: invalid rclone file header")
	}
	var nonce [FileNonceSize]byte
	copy(nonce[:], header[FileMagicSize:])
	d.nonceCache.Store(entry.ID, nonce)
	return nonce, nil
}

type discardPrefixReader struct {
	reader  io.Reader
	discard int64
	closer  io.Closer
	done    bool
}

func (r *discardPrefixReader) Read(p []byte) (int, error) {
	if !r.done {
		if _, err := io.CopyN(io.Discard, r.reader, r.discard); err != nil {
			return 0, err
		}
		r.done = true
	}
	return r.reader.Read(p)
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	encName := d.cp.EncryptSegment(name)
	entry, err := d.raw.Mkdir(ctx, parentID, encName)
	if err == nil {
		entry.Extra = EntryExtra{RemoteName: encName, RawExtra: entry.Extra}
		entry.Name = name
	}
	return entry, err
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	return d.raw.Move(ctx, entry, dstParentID)
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	return d.raw.Rename(ctx, entry, d.cp.EncryptSegment(newName))
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	return d.raw.Remove(ctx, entry)
}

func (d *Driver) ResolvePath(ctx context.Context, path string) (string, error) {
	if path == "" || path == "/" {
		return d.raw.ResolvePath(ctx, path)
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part != "" {
			parts[i] = d.cp.EncryptSegment(part)
		}
	}
	return d.raw.ResolvePath(ctx, "/"+strings.Join(parts, "/"))
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	source := req.Source
	nonce, err := d.nonceForSource(source)
	if err != nil {
		return drive.Entry{}, err
	}
	encName := d.cp.EncryptSegment(req.Name)
	encSource := newEncryptedReadOnlyFileSource(source, d.cp, nonce, nil)
	if d.contentDedup {
		if requiredHashes := d.raw.RequiredUploadHashes(); len(requiredHashes) > 0 {
			hashes, ok := d.cachedUploadHashes(source, requiredHashes)
			if !ok {
				var err error
				hashes, err = readSourceHashes(ctx, encSource, requiredHashes)
				if err != nil {
					return drive.Entry{}, err
				}
				d.storeUploadHashes(source, hashes)
			}
			encSource = newEncryptedReadOnlyFileSource(source, d.cp, nonce, hashes)
		}
	}
	rawReq := req
	rawReq.Name = encName
	rawReq.Source = encSource
	entry, err := d.raw.PutSource(ctx, rawReq)
	if err == nil {
		d.nonceCache.Store(entry.ID, nonce)
		entry.Extra = EntryExtra{RemoteName: encName, RawExtra: entry.Extra}
		entry.Name = req.Name
		entry.Size = source.Size()
	}
	return entry, err
}

func (d *Driver) RequiredUploadHashes() []drive.HashAlgorithm {
	if d.contentDedup {
		return []drive.HashAlgorithm{drive.HashSHA256}
	}
	return nil
}

func (d *Driver) cachedUploadHashes(source drive.ReadOnlyFileSource, algorithms []drive.HashAlgorithm) (drive.SourceHashes, bool) {
	key, ok := contentDedupHashCacheKey(source)
	if !ok {
		return nil, false
	}
	value, ok := d.hashCache.Load(key)
	if !ok {
		return nil, false
	}
	hashes := value.(drive.SourceHashes)
	if !hasAllHashes(hashes, algorithms) {
		return nil, false
	}
	return cloneHashes(hashes), true
}

func (d *Driver) storeUploadHashes(source drive.ReadOnlyFileSource, hashes drive.SourceHashes) {
	if len(hashes) == 0 {
		return
	}
	key, ok := contentDedupHashCacheKey(source)
	if !ok {
		return
	}
	d.hashCache.Store(key, cloneHashes(hashes))
}

func contentDedupHashCacheKey(source drive.ReadOnlyFileSource) (string, bool) {
	sum, ok := drive.SourceHash(source, drive.HashSHA256)
	if !ok || len(sum) != sha256.Size {
		return "", false
	}
	return fmt.Sprintf("%d:%s", source.Size(), hex.EncodeToString(sum)), true
}

func hasAllHashes(hashes drive.SourceHashes, algorithms []drive.HashAlgorithm) bool {
	for _, algorithm := range algorithms {
		if _, ok := hashes[algorithm]; !ok {
			return false
		}
	}
	return true
}

func cloneHashes(hashes drive.SourceHashes) drive.SourceHashes {
	if len(hashes) == 0 {
		return nil
	}
	cloned := make(drive.SourceHashes, len(hashes))
	for algorithm, sum := range hashes {
		cloned[algorithm] = append([]byte(nil), sum...)
	}
	return cloned
}

func readSourceHashes(ctx context.Context, source drive.ReadOnlyFileSource, algorithms []drive.HashAlgorithm) (drive.SourceHashes, error) {
	if len(algorithms) == 0 {
		return nil, nil
	}
	hashes := make(drive.SourceHashes, len(algorithms))
	writers := make([]io.Writer, 0, len(algorithms))
	byAlgorithm := map[drive.HashAlgorithm]hash.Hash{}
	for _, algorithm := range algorithms {
		if _, ok := byAlgorithm[algorithm]; ok {
			continue
		}
		var h hash.Hash
		switch algorithm {
		case drive.HashMD5:
			h = md5.New()
		case drive.HashSHA1:
			h = sha1.New()
		case drive.HashSHA256:
			h = sha256.New()
		default:
			return nil, fmt.Errorf("crypt: unsupported upload hash algorithm %q", algorithm)
		}
		byAlgorithm[algorithm] = h
		writers = append(writers, h)
	}
	if len(writers) == 0 {
		return nil, nil
	}
	file, err := source.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("crypt: upload hash source open: %w", err)
	}
	written, copyErr := io.Copy(io.MultiWriter(writers...), file)
	closeErr := file.Close()
	if copyErr != nil {
		return nil, fmt.Errorf("crypt: upload hash source read: %w", copyErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("crypt: upload hash source close: %w", closeErr)
	}
	if written != source.Size() {
		return nil, fmt.Errorf("crypt: upload hash source size mismatch: read %d, expected %d", written, source.Size())
	}
	for algorithm, h := range byAlgorithm {
		hashes[algorithm] = h.Sum(nil)
	}
	return hashes, nil
}

func (d *Driver) nonceForSource(source drive.ReadOnlyFileSource) ([FileNonceSize]byte, error) {
	if !d.contentDedup {
		nonce, err := d.cp.GenerateRandomNonce()
		if err != nil {
			return [FileNonceSize]byte{}, fmt.Errorf("crypt: generate nonce: %w", err)
		}
		return nonce, nil
	}
	sumBytes, ok := drive.SourceHash(source, drive.HashSHA256)
	if !ok {
		return [FileNonceSize]byte{}, errors.New("crypt: content_dedup requires source SHA-256 metadata")
	}
	if len(sumBytes) != sha256.Size {
		return [FileNonceSize]byte{}, fmt.Errorf("crypt: source SHA-256 metadata has %d bytes, want %d", len(sumBytes), sha256.Size)
	}
	var sum [sha256.Size]byte
	copy(sum[:], sumBytes)
	nonce, err := d.cp.ContentDedupNonce(sum, source.Size())
	if err != nil {
		return [FileNonceSize]byte{}, fmt.Errorf("crypt: derive content_dedup nonce: %w", err)
	}
	return nonce, nil
}

var _ drive.Driver = (*Driver)(nil)
