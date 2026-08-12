package mobile

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	handle, err := p.opener.Open(token, offset)
	if err != nil {
		return nil, wrapError(err)
	}
	return &mobileSourceReader{opener: p.opener, handle: handle}, nil
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
