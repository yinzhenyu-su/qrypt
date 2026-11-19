package cassette

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// FormatVersion is the cassette schema version written by Recorder and accepted
// by Replayer. Bump it when a field changes meaning; old cassettes must then be
// re-recorded rather than silently reinterpreted.
const FormatVersion = 1

// ChunkSize is the body chunk granularity recorded per request. It matches the
// read layer's drive.ReadChunkSize, so chunk timestamps line up with the
// streaming delivery boundaries the provider is measured at.
const ChunkSize = drive.ReadChunkSize

// Cassette is a versioned recording of provider read traffic. It is plain JSON
// so an incident capture can be committed as testdata and reviewed in a diff.
type Cassette struct {
	// Version is the schema version; see FormatVersion.
	Version int `json:"version"`
	// Notes is free-form context (incident link, provider, date).
	Notes string `json:"notes,omitempty"`
	// Files are the objects the recording read from.
	Files []FileRecord `json:"files"`
	// Requests are the provider Range calls in the order they were observed.
	Requests []RequestRecord `json:"requests"`
}

// FileRecord describes one object the recording touched. Size is the entry size
// the read layer planned against, which matters because it drives window
// splitting decisions.
type FileRecord struct {
	ID           string             `json:"id"`
	Name         string             `json:"name,omitempty"`
	Size         int64              `json:"size"`
	Capabilities []drive.Capability `json:"capabilities,omitempty"`
}

// RequestRecord is one provider Range request. The recording captures the open
// latency, each body chunk with its delivery time, and how the request ended,
// which is enough to replay timeouts, throttling and truncation faithfully.
type RequestRecord struct {
	// EntryID, Offset and Size are the exact request shape replay matches on.
	EntryID string `json:"entry_id"`
	Offset  int64  `json:"offset"`
	Size    int64  `json:"size"`
	// Attempt counts repeated provider calls for the same (entry, offset, size)
	// within one session; replay preserves order with it.
	Attempt int `json:"attempt,omitempty"`
	// Concurrent is how many reads were in flight when this one opened.
	Concurrent int `json:"concurrent,omitempty"`
	// OpenNS is the time from Read call to the driver returning a body (TTFB).
	OpenNS int64 `json:"open_ns,omitempty"`
	// FirstByteNS is the time from body hand-off to the first body bytes. It is
	// part of OpenNS for buffered reads; it lets a streaming reconstruction see
	// a body that opened fast but stalled on its first read.
	FirstByteNS int64 `json:"first_byte_ns,omitempty"`
	// CloseNS is the body lifetime: from hand-off until EOF, error or Close.
	CloseNS int64 `json:"close_ns,omitempty"`
	// Chunks are the body bytes in delivery order.
	Chunks []ChunkRecord `json:"chunks,omitempty"`
	// Status is the HTTP status for ErrorKindHTTPStatus; zero otherwise.
	Status int `json:"status,omitempty"`
	// ErrorKind classifies an error returned by Read (the open), if any.
	ErrorKind string `json:"error_kind,omitempty"`
	// Error is the recorded open error text, for debugging.
	Error string `json:"error,omitempty"`
	// BodyErrorKind classifies an error returned mid-body, if any.
	BodyErrorKind string `json:"body_error_kind,omitempty"`
	// BodyError is the recorded mid-body error text.
	BodyError string `json:"body_error,omitempty"`
	// Incomplete marks a body the recorder closed before EOF. Replay reproduces
	// the short body exactly; reading past it returns ErrIncompleteRecord.
	Incomplete bool `json:"incomplete,omitempty"`
}

// ChunkRecord is one recorded body chunk.
type ChunkRecord struct {
	// Data is the chunk payload (base64 in JSON).
	Data []byte `json:"data"`
	// AtNS is when the chunk became readable, in nanoseconds relative to body
	// hand-off.
	AtNS int64 `json:"at_ns"`
}

// Validate checks that the cassette is internally consistent enough to replay
// deterministically. It rejects unknown versions, duplicate files, empty range
// requests, non-monotonic chunk timing, bodies larger than the requested range
// and error fields that contradict the body.
func (c *Cassette) Validate() error {
	if c == nil {
		return errors.New("cassette: nil cassette")
	}
	if c.Version != FormatVersion {
		return fmt.Errorf("cassette: unsupported version %d (want %d)", c.Version, FormatVersion)
	}
	if len(c.Files) == 0 {
		return errors.New("cassette: no files recorded")
	}
	seenFiles := make(map[string]struct{}, len(c.Files))
	for i, file := range c.Files {
		if file.ID == "" {
			return fmt.Errorf("cassette: file %d has empty id", i)
		}
		if _, ok := seenFiles[file.ID]; ok {
			return fmt.Errorf("cassette: duplicate file id %q", file.ID)
		}
		seenFiles[file.ID] = struct{}{}
		if file.Size < 0 {
			return fmt.Errorf("cassette: file %q has negative size", file.ID)
		}
	}
	for i := range c.Requests {
		if err := c.Requests[i].validate(); err != nil {
			return fmt.Errorf("cassette: request %d: %w", i, err)
		}
	}
	return nil
}

// validate checks one request record.
func (r *RequestRecord) validate() error {
	if r.EntryID == "" {
		return errors.New("empty entry id")
	}
	if r.Offset < 0 || r.Size < 0 {
		return fmt.Errorf("negative range offset %d size %d", r.Offset, r.Size)
	}
	if r.Attempt < 0 || r.Concurrent < 0 {
		return fmt.Errorf("negative attempt %d or concurrency %d", r.Attempt, r.Concurrent)
	}
	if r.OpenNS < 0 || r.FirstByteNS < 0 || r.CloseNS < 0 {
		return errors.New("negative timing")
	}
	if !validErrorKind(r.ErrorKind) {
		return fmt.Errorf("unknown error kind %q", r.ErrorKind)
	}
	if !validErrorKind(r.BodyErrorKind) {
		return fmt.Errorf("unknown body error kind %q", r.BodyErrorKind)
	}
	if !validStatus(r.Status) {
		return fmt.Errorf("implausible status %d", r.Status)
	}
	if r.ErrorKind == ErrorKindHTTPStatus && r.Status == 0 {
		return errors.New("http_status error without status")
	}
	if r.ErrorKind == "" && r.Status != 0 {
		return errors.New("status without error kind")
	}
	if r.ErrorKind != "" && len(r.Chunks) > 0 {
		return errors.New("open error with body chunks")
	}
	if r.BodyErrorKind != "" && r.Incomplete {
		return errors.New("body error and incomplete body together")
	}
	var total int64
	for j, chunk := range r.Chunks {
		if len(chunk.Data) == 0 {
			return fmt.Errorf("chunk %d is empty", j)
		}
		if len(chunk.Data) > ChunkSize {
			return fmt.Errorf("chunk %d is %d bytes, larger than %d", j, len(chunk.Data), ChunkSize)
		}
		if chunk.AtNS < 0 || (j > 0 && chunk.AtNS < r.Chunks[j-1].AtNS) {
			return fmt.Errorf("chunk %d has non-monotonic at_ns %d", j, chunk.AtNS)
		}
		total += int64(len(chunk.Data))
	}
	if total > r.Size {
		return fmt.Errorf("recorded %d body bytes for a %d byte range", total, r.Size)
	}
	return nil
}

// Marshal encodes the cassette as indented JSON with a trailing newline, after
// validating it. The indentation keeps recorded testdata reviewable.
func (c *Cassette) Marshal() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cassette: marshal: %w", err)
	}
	return append(data, '\n'), nil
}

// Parse decodes and validates a cassette from JSON.
func Parse(data []byte) (*Cassette, error) {
	var c Cassette
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("cassette: parse: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// LoadFile reads, decodes and validates a cassette file.
func LoadFile(path string) (*Cassette, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cassette: read %s: %w", path, err)
	}
	return Parse(data)
}

// SaveFile validates the cassette and writes it as indented JSON. Recorded
// cassettes are ordinary files, so they can live in testdata and be diffed.
func (c *Cassette) SaveFile(path string) error {
	data, err := c.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("cassette: write %s: %w", path, err)
	}
	return nil
}

// FilesByID indexes the recorded files by entry ID.
func (c *Cassette) FilesByID() map[string]FileRecord {
	files := make(map[string]FileRecord, len(c.Files))
	for _, file := range c.Files {
		files[file.ID] = file
	}
	return files
}
