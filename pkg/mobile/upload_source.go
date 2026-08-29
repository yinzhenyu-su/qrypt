package mobile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// UploadSourceOpener is implemented by the embedding app (through gomobile
// bind) so direct upload tasks can reopen an app-owned source later. qrypt
// opens the source at least twice per upload: once to compute hashes for
// instant upload, then again (skipping to the requested offset) for the
// cloud upload itself, so the opener must support opening at any offset.
//
// On Android the implementation maps token to
// ContentResolver.openAssetFileDescriptor(uri, "r") and seeks to offset.
//
// Read returns a freshly allocated byte slice instead of filling a caller
// buffer: gobind passes []byte method arguments to Java one-way, so bytes
// written into a caller-supplied buffer would never reach Go.
type UploadSourceOpener interface {
	// Open opens the source identified by token at the given byte offset and
	// returns a handle for subsequent Read/Close calls. The handle stays
	// valid until Close is called.
	Open(token string, offset int64) (int64, error)
	// Read reads up to size bytes from the open handle, starting at the
	// current position. An empty slice signals end of stream.
	Read(handle int64, size int) ([]byte, error)
	// Close releases the handle.
	Close(handle int64) error
	// OpenRawSource hands over a raw file descriptor for the source (e.g.
	// from ParcelFileDescriptor.detachFd) plus the byte offset that fd starts
	// at, so Go can read the source directly without a per-read JNI round
	// trip. The fd must be seekable; ownership transfers to Go, which closes
	// it. Any failure (unsupported provider, pipe-backed fd, detach error)
	// falls back to Open/Read above, so this path is purely an optimization.
	//
	// The result is a bound struct rather than a tuple: gobind interface
	// proxies do not support int64 slices.
	OpenRawSource(token string) (*RawSource, error)
}

// RawSource is the raw file descriptor handed to Go for direct source reads.
// FD is owned by Go once returned; StartOffset is the byte offset the fd
// begins at (e.g. a document container start offset).
type RawSource struct {
	FD          int64
	StartOffset int64
}

var uploadSourceMu sync.Mutex

// uploadSourceOpener is the app-provided opener shared by all sessions. It is
// installed via SetUploadSourceOpenerJSON and re-applied to every session
// opened after it (sessions opened before the call are updated in place).
var uploadSourceOpener UploadSourceOpener

// SetUploadSourceOpenerJSON installs the app-provided source opener for all
// current and future sessions so direct upload tasks can reopen app-owned
// sources (SAF/content URIs on Android). Returns {"ok":true}.
func SetUploadSourceOpenerJSON(opener UploadSourceOpener) string {
	if opener == nil {
		return resultJSON(nil, wrapError(errors.New("mobile: upload source opener must not be nil")))
	}
	uploadSourceMu.Lock()
	uploadSourceOpener = opener
	uploadSourceMu.Unlock()
	registry.mu.Lock()
	sessions := make([]*session, 0, len(registry.sessions))
	for _, s := range registry.sessions {
		sessions = append(sessions, s)
	}
	registry.mu.Unlock()
	for _, s := range sessions {
		s.applyUploadSourceProvider(opener)
	}
	return resultJSON(true, nil)
}

// ClearUploadSourceOpenerJSON removes the app-provided source opener from all
// current and future sessions. Direct uploads then fall back to qrypt-readable
// local filesystem paths.
func ClearUploadSourceOpenerJSON() string {
	uploadSourceMu.Lock()
	uploadSourceOpener = nil
	uploadSourceMu.Unlock()
	registry.mu.Lock()
	sessions := make([]*session, 0, len(registry.sessions))
	for _, s := range registry.sessions {
		sessions = append(sessions, s)
	}
	registry.mu.Unlock()
	for _, s := range sessions {
		s.applyUploadSourceProvider(nil)
	}
	return resultJSON(true, nil)
}

func currentUploadSourceOpener() UploadSourceOpener {
	uploadSourceMu.Lock()
	defer uploadSourceMu.Unlock()
	return uploadSourceOpener
}

// applyUploadSourceProvider wires the opener-backed provider into the core so
// direct upload tasks can reopen app-owned sources. Called when a session is
// opened and when the opener is (re)installed.
func (s *session) applyUploadSourceProvider(opener UploadSourceOpener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.core != nil {
		s.core.SetUploadSourceProvider(&mobileUploadSourceProvider{opener: opener})
	}
}

// mobileUploadSourceProvider adapts the app-provided UploadSourceOpener to
// core.UploadSourceProvider.
type mobileUploadSourceProvider struct {
	opener UploadSourceOpener
}

func (p *mobileUploadSourceProvider) OpenUploadSource(ctx context.Context, token string, offset int64) (io.ReadCloser, error) {
	if p.opener == nil {
		return nil, fmt.Errorf("mobile: no upload source opener registered")
	}
	// Preferred path: the app hands over a raw file descriptor so Go reads
	// the source directly with no per-read JNI round trip or byte copying.
	// Any failure (unsupported provider, non-seekable fd) falls back below.
	if raw, err := p.opener.OpenRawSource(token); err == nil && raw != nil && raw.FD > 0 {
		if f, seekErr := openSeekableFD(raw.FD, raw.StartOffset+offset); seekErr == nil {
			return f, nil
		}
	}
	handle, err := p.opener.Open(token, offset)
	if err != nil {
		return nil, wrapError(err)
	}
	return &mobileSourceReader{opener: p.opener, handle: handle}, nil
}

// fdSourceReader reads a raw file descriptor handed over by the app. The seek
// in openSeekableFD doubles as a seekability probe: a pipe-backed source
// (which cannot honor requested offsets) fails here and the caller falls back
// to the per-read opener path.
type fdSourceReader struct {
	file   *os.File
	closed bool
}

func openSeekableFD(fd, offset int64) (io.ReadCloser, error) {
	if fd <= 0 {
		return nil, fmt.Errorf("mobile: invalid source fd %d", fd)
	}
	file := os.NewFile(uintptr(fd), "qrypt-upload-source")
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &fdSourceReader{file: file}, nil
}

func (r *fdSourceReader) Read(p []byte) (int, error) { return r.file.Read(p) }

func (r *fdSourceReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.file.Close()
}

type mobileSourceReader struct {
	opener UploadSourceOpener
	handle int64
	closed bool
}

func (r *mobileSourceReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.closed {
		return 0, io.EOF
	}
	data, err := r.opener.Read(r.handle, len(p))
	if err != nil {
		return 0, wrapError(err)
	}
	if len(data) == 0 {
		return 0, io.EOF
	}
	if len(data) > len(p) {
		return 0, fmt.Errorf("mobile: upload source read returned %d bytes for requested %d", len(data), len(p))
	}
	n := copy(p, data)
	return n, nil
}

func (r *mobileSourceReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return wrapError(r.opener.Close(r.handle))
}
