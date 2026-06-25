package osutil

import (
	"io"
	"os"
)

// OpenRead opens path and seeks to offset.  If size > 0 the returned
// ReadCloser reads at most size bytes; otherwise it reads to EOF.
func OpenRead(path string, offset, size int64) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	if size > 0 {
		return struct {
			io.Reader
			io.Closer
		}{Reader: io.LimitReader(f, size), Closer: f}, nil
	}
	return f, nil
}
