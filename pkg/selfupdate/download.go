package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Downloader streams release assets to disk. HTTP is a field so tests can
// drive it with a local server's client.
type Downloader struct {
	HTTP *http.Client
}

// Download streams rawURL to dst and returns the number of bytes written. A
// response shorter than its declared length is reported as a truncated
// download rather than silently accepted.
func (d *Downloader) Download(ctx context.Context, rawURL, dst string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := d.http().Do(req)
	if err != nil {
		return 0, fmt.Errorf("download %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download %s: unexpected status %s", rawURL, resp.Status)
	}

	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return written, fmt.Errorf("download %s: %w", rawURL, copyErr)
	}
	if closeErr != nil {
		return written, closeErr
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		return written, fmt.Errorf("download %s: truncated, got %d of %d bytes", rawURL, written, resp.ContentLength)
	}
	return written, nil
}

func (d *Downloader) http() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return http.DefaultClient
}
