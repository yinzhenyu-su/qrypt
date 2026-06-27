package p115

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type Driver struct {
	cl     *driver115.Pan115Client
	rootID string

	debugMu sync.Mutex
}

func init() {
	drive.Register("115", func(params drive.Params) (drive.Driver, error) {
		cookie := params["cookie"]
		if cookie == "" {
			return nil, fmt.Errorf("115: missing cookie")
		}
		return New(Options{
			Cookie: cookie,
			RootID: params["root_id"],
		}), nil
	})
}

type Options struct {
	Cookie string
	RootID string
}

func New(opts Options) *Driver {
	d := &Driver{rootID: opts.RootID}
	if opts.Cookie != "" {
		cl := driver115.New()
		cred := &driver115.Credential{}
		if err := cred.FromCookie(opts.Cookie); err == nil {
			cl.ImportCredential(cred)
		} else {
			cl.ImportCookies(cookieMap(opts.Cookie), "115.com")
		}
		d.cl = cl
	}
	return d
}

func cookieMap(s string) map[string]string {
	m := map[string]string{}
	for _, item := range splitCookies(s) {
		k, v, ok := cut(item, "=")
		if ok {
			m[trim(k)] = trim(v)
		}
	}
	return m
}

func splitCookies(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ';' {
			parts = append(parts, s[start:i])
			start = i + 2
		}
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

func cut(s, sep string) (string, string, bool) {
	for i := 0; i < len(s)-len(sep)+1; i++ {
		if s[i:i+len(sep)] == sep {
			return trim(s[:i]), trim(s[i+len(sep):]), true
		}
	}
	return "", "", false
}

func trim(s string) string {
	l, r := 0, len(s)
	for l < r && (s[l] == ' ' || s[l] == '\t') {
		l++
	}
	for r > l && (s[r-1] == ' ' || s[r-1] == '\t') {
		r--
	}
	return s[l:r]
}

func (d *Driver) Init(ctx context.Context) error {
	if d.cl == nil {
		return fmt.Errorf("115: not initialized")
	}
	if err := d.cl.LoginCheck(); err != nil {
		return fmt.Errorf("115: login check: %w", err)
	}
	if d.rootID == "" {
		d.rootID = "0"
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error { return nil }

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	fileID := parentID
	if fileID == "" || fileID == "0" || fileID == "/" {
		fileID = d.rootID
	}
	files, err := d.cl.List(fileID)
	if err != nil {
		return nil, fmt.Errorf("115: list: %w", err)
	}
	entries := make([]drive.Entry, 0, len(*files))
	for _, f := range *files {
		entries = append(entries, drive.Entry{
			ID:       f.FileID,
			ParentID: fileID,
			Name:     f.Name,
			IsDir:    f.IsDir(),
			Size:     f.Size,
			ModTime:  f.ModTime(),
		})
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	files, err := d.cl.List(entry.ParentID)
	if err != nil {
		return nil, fmt.Errorf("115: read list: %w", err)
	}
	var pickCode string
	for _, f := range *files {
		if f.FileID == entry.ID {
			pickCode = f.PickCode
			break
		}
	}
	if pickCode == "" {
		return nil, fmt.Errorf("115: read: pick code not found for %s", entry.ID)
	}

	info, err := d.cl.DownloadWithUA(pickCode, "")
	if err != nil {
		return nil, fmt.Errorf("115: read download: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.Url.Url, nil)
	if err != nil {
		return nil, fmt.Errorf("115: read req: %w", err)
	}
	for k, vs := range info.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if size > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("115: read do: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("115: read status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	fileID := parentID
	if fileID == "" || fileID == "0" || fileID == "/" {
		fileID = d.rootID
	}
	newID, err := d.cl.Mkdir(fileID, name)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("115: mkdir: %w", err)
	}
	return drive.Entry{ID: newID, ParentID: fileID, Name: name, IsDir: true}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	return d.cl.Move(entry.ID, dstParentID)
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	return d.cl.Rename(entry.ID, newName)
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	return d.cl.Delete(entry.ID)
}
