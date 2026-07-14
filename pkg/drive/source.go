package drive

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"io"
	"os"
)

// ReadOnlyFile is a stable, seekable, read-only file handle.
type ReadOnlyFile interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
}

// ReadOnlyFileSource opens stable, read-only handles over one upload source.
// Implementations may return a fresh handle on each Open call. Callers must
// close every opened handle.
type ReadOnlyFileSource interface {
	Size() int64
	Open(ctx context.Context) (ReadOnlyFile, error)
}

type HashAlgorithm string

const (
	HashMD5    HashAlgorithm = "md5"
	HashSHA1   HashAlgorithm = "sha1"
	HashSHA256 HashAlgorithm = "sha256"
)

type SourceHashes map[HashAlgorithm][]byte

// HashProvider is an optional source metadata interface for callers that
// already computed content hashes while preparing the source.
type HashProvider interface {
	Hash(algorithm HashAlgorithm) ([]byte, bool)
}

type UploadPhase string

const (
	UploadPhasePreparing  UploadPhase = "preparing"
	UploadPhaseHashing    UploadPhase = "hashing"
	UploadPhaseUploading  UploadPhase = "uploading"
	UploadPhaseInstant    UploadPhase = "instant"
	UploadPhaseCommitting UploadPhase = "committing"
	UploadPhaseCompleted  UploadPhase = "completed"
)

// UploadProgress receives progress for the logical upload represented by an
// UploadRequest. Implementations must be safe to call repeatedly with small
// positive byte deltas.
type UploadProgress interface {
	Phase(UploadPhase)
	Uploaded(n int64)
}

type UploadRequest struct {
	ParentID string
	Name     string
	Source   ReadOnlyFileSource
	Progress UploadProgress
}

func SourceHash(source ReadOnlyFileSource, algorithm HashAlgorithm) ([]byte, bool) {
	provider, ok := source.(HashProvider)
	if !ok {
		return nil, false
	}
	sum, ok := provider.Hash(algorithm)
	if !ok {
		return nil, false
	}
	return append([]byte(nil), sum...), true
}

type localReadOnlyFileSource struct {
	path   string
	size   int64
	hashes SourceHashes
}

// NewLocalReadOnlyFileSource adapts a local path into a read-only upload source.
func NewLocalReadOnlyFileSource(path string, size int64) ReadOnlyFileSource {
	return localReadOnlyFileSource{path: path, size: size}
}

// NewLocalReadOnlyFileSourceWithHashes adapts a local path into a read-only
// upload source and attaches previously computed content hashes.
func NewLocalReadOnlyFileSourceWithHashes(path string, size int64, hashes SourceHashes) ReadOnlyFileSource {
	return localReadOnlyFileSource{path: path, size: size, hashes: cloneSourceHashes(hashes)}
}

func (s localReadOnlyFileSource) Size() int64 {
	return s.size
}

func (s localReadOnlyFileSource) Open(ctx context.Context) (ReadOnlyFile, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return os.Open(s.path)
}

func (s localReadOnlyFileSource) Hash(algorithm HashAlgorithm) ([]byte, bool) {
	return sourceHash(s.hashes, algorithm)
}

type bytesReadOnlyFileSource struct {
	data   []byte
	hashes SourceHashes
}

// NewBytesReadOnlyFileSource adapts an immutable byte slice into a read-only
// upload source. The input is copied so later caller mutations cannot affect
// the upload.
func NewBytesReadOnlyFileSource(data []byte) ReadOnlyFileSource {
	copied := append([]byte(nil), data...)
	return bytesReadOnlyFileSource{data: copied, hashes: hashBytes(copied)}
}

func (s bytesReadOnlyFileSource) Size() int64 {
	return int64(len(s.data))
}

func (s bytesReadOnlyFileSource) Open(ctx context.Context) (ReadOnlyFile, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return readSeekCloser{Reader: bytes.NewReader(s.data)}, nil
}

func (s bytesReadOnlyFileSource) Hash(algorithm HashAlgorithm) ([]byte, bool) {
	return sourceHash(s.hashes, algorithm)
}

type readSeekCloser struct {
	*bytes.Reader
}

func (readSeekCloser) Close() error {
	return nil
}

var _ ReadOnlyFile = readSeekCloser{}
var _ io.Reader = readSeekCloser{}

func ReportUploadProgress(progress UploadProgress, n int64) {
	if n <= 0 {
		return
	}
	if progress != nil {
		progress.Uploaded(n)
	}
}

func ReportUploadPhase(progress UploadProgress, phase UploadPhase) {
	if progress != nil && phase != "" {
		progress.Phase(phase)
	}
}

func NewUploadProgressReader(progress UploadProgress, reader io.Reader) io.Reader {
	if reader == nil {
		return nil
	}
	ReportUploadPhase(progress, UploadPhaseUploading)
	if seeker, ok := reader.(io.ReadSeeker); ok {
		return uploadProgressReadSeeker{progress: progress, reader: seeker}
	}
	return uploadProgressReader{progress: progress, reader: reader}
}

type uploadProgressReader struct {
	progress UploadProgress
	reader   io.Reader
}

func (r uploadProgressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	ReportUploadProgress(r.progress, int64(n))
	return n, err
}

type uploadProgressReadSeeker struct {
	progress UploadProgress
	reader   io.ReadSeeker
}

func (r uploadProgressReadSeeker) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	ReportUploadProgress(r.progress, int64(n))
	return n, err
}

func (r uploadProgressReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return r.reader.Seek(offset, whence)
}

func hashBytes(data []byte) SourceHashes {
	md5Sum := md5.Sum(data)
	sha1Sum := sha1.Sum(data)
	sha256Sum := sha256.Sum256(data)
	return SourceHashes{
		HashMD5:    md5Sum[:],
		HashSHA1:   sha1Sum[:],
		HashSHA256: sha256Sum[:],
	}
}

func sourceHash(hashes SourceHashes, algorithm HashAlgorithm) ([]byte, bool) {
	sum, ok := hashes[algorithm]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), sum...), true
}

func cloneSourceHashes(hashes SourceHashes) SourceHashes {
	if len(hashes) == 0 {
		return nil
	}
	cloned := make(SourceHashes, len(hashes))
	for algorithm, sum := range hashes {
		cloned[algorithm] = append([]byte(nil), sum...)
	}
	return cloned
}
