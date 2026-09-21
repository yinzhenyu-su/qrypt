package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestCopyUploadSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		source    drive.ReadOnlyFileSource
		writer    uploadWriter
		cancel    bool
		want      string
		wantError error
	}{
		{
			name:   "copies and commits",
			source: drive.NewBytesReadOnlyFileSource([]byte("payload")),
			writer: &recordingUploadWriter{},
			want:   "payload",
		},
		{
			name:      "requires source",
			writer:    &recordingUploadWriter{},
			wantError: errors.New("core: upload source required"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer, ok := tt.writer.(*recordingUploadWriter)
			if !ok {
				t.Fatal("test writer has unexpected type")
			}
			_, err := copyUploadSource(context.Background(), tt.source, writer)
			if tt.wantError != nil {
				if err == nil || err.Error() != tt.wantError.Error() {
					t.Fatalf("copyUploadSource error = %v, want %v", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("copyUploadSource error = %v", err)
			}
			if writer.data.String() != tt.want {
				t.Fatalf("copied data = %q, want %q", writer.data.String(), tt.want)
			}
			if !writer.committed {
				t.Fatal("writer was not committed")
			}
		})
	}
}

func TestCopyUploadSourceRejectsShortWrite(t *testing.T) {
	t.Parallel()
	writer := &recordingUploadWriter{shortWrite: true}
	_, err := copyUploadSource(context.Background(), drive.NewBytesReadOnlyFileSource([]byte("payload")), writer)
	if err == nil || !strings.Contains(err.Error(), "short staging write") {
		t.Fatalf("copyUploadSource error = %v, want short staging write", err)
	}
	if writer.committed {
		t.Fatal("short write committed the writer")
	}
}

func TestCopyUploadSourceStopsOnCanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writer := &recordingUploadWriter{}
	_, err := copyUploadSource(ctx, drive.NewBytesReadOnlyFileSource([]byte("payload")), writer)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copyUploadSource error = %v, want context.Canceled", err)
	}
	if writer.committed {
		t.Fatal("canceled copy committed the writer")
	}
}

func TestCopyUploadSourceReturnsReaderError(t *testing.T) {
	t.Parallel()
	writer := &recordingUploadWriter{}
	_, err := copyUploadSource(context.Background(), errorUploadSource{}, writer)
	if err == nil || !strings.Contains(err.Error(), "read upload source") {
		t.Fatalf("copyUploadSource error = %v, want source read error", err)
	}
	if writer.committed {
		t.Fatal("source read failure committed the writer")
	}
}

type recordingUploadWriter struct {
	data       strings.Builder
	committed  bool
	shortWrite bool
}

func (w *recordingUploadWriter) WriteAt(_ context.Context, data []byte, _ int64) (int, error) {
	if w.shortWrite {
		return len(data) - 1, nil
	}
	_, err := w.data.Write(data)
	return len(data), err
}

func (w *recordingUploadWriter) Commit(context.Context) (drive.Entry, error) {
	w.committed = true
	return drive.Entry{Size: int64(w.data.Len())}, nil
}

func (*recordingUploadWriter) Abort(context.Context) error { return nil }

type errorUploadSource struct{}

func (errorUploadSource) Size() int64 { return 1 }

func (errorUploadSource) Open(context.Context) (drive.ReadOnlyFile, error) {
	return errorUploadFile{}, nil
}

type errorUploadFile struct{}

func (errorUploadFile) Read([]byte) (int, error) { return 0, errors.New("source read failed") }

func (errorUploadFile) ReadAt([]byte, int64) (int, error) { return 0, errors.New("source read failed") }

func (errorUploadFile) Seek(int64, int) (int64, error) { return 0, nil }

func (errorUploadFile) Close() error { return nil }
