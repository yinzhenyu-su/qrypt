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
