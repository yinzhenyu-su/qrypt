// Package webdav implements a WebDAV backend driver for qrypt.
//
// It communicates with any standard WebDAV server (NextCloud, ownCloud,
// Apache mod_dav, etc.) over HTTP using the standard WebDAV method set:
//
//   - PROPFIND — list directory / get file properties
//   - GET      — read file contents (with Range support)
//   - PUT      — upload / create file
//   - MKCOL    — create directory
//   - DELETE   — remove file or empty directory
//   - MOVE     — move / rename
//
// Authentication is via HTTP Basic Auth.  The driver does not support
// Digest auth, OAuth, or cookie-based SSO — use a reverse proxy or
// auth header middleware if your server requires those.
package webdav

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ─── XML types for PROPFIND responses ────────────────────────────────────

// multistatus is the root element of a PROPFIND response.
type multistatus struct {
	XMLName   xml.Name           `xml:"DAV: multistatus"`
	Responses []propfindResponse `xml:"response"`
}

type propfindResponse struct {
	Href     string     `xml:"href"`
	Propstat []propstat `xml:"propstat"`
}

type propstat struct {
	Prop   prop   `xml:"prop"`
	Status string `xml:"status"`
}

type prop struct {
	ResourceType  *resourceType `xml:"resourcetype"`
	GetContentLen string        `xml:"getcontentlength"`
	GetLastMod    string        `xml:"getlastmodified"`
	DisplayName   string        `xml:"displayname"`
	GetETag       string        `xml:"getetag"`
}

type resourceType struct {
	Collection *struct{} `xml:"collection"`
}

// ─── Driver ──────────────────────────────────────────────────────────────

// Driver implements drive.Driver (plus Writer and Uploader) for WebDAV.
//
// It uses URL-path-based IDs: the root is "/", a subdirectory "/photos",
// a file "/photos/vacation.jpg".  Internally these are mapped to full
// WebDAV URLs by prepending baseURL.
type Driver struct {
	baseURL  string // WebDAV root, always ends with "/"
	rootPath string
	username string
	password string
	client   *http.Client
}

// Options for creating a new WebDAV driver.
type Options struct {
	URL      string // e.g. "https://nextcloud.example.com/remote.php/dav/files/user"
	Username string
	Password string
	RootPath string
}

func init() {
	drive.Register("webdav", func(params drive.Params) (drive.Driver, error) {
		url := params["url"]
		if url == "" {
			return nil, fmt.Errorf("webdav: missing url")
		}
		return New(Options{
			URL:      url,
			Username: params["username"],
			Password: params["password"],
			RootPath: params["root_path"],
		}), nil
	},
		drive.ParamDef{
			Name:        "url",
			Type:        "string",
			Required:    true,
			Description: "WebDAV server base URL",
			Example:     "https://nextcloud.example.com/remote.php/dav/files/user",
		},
		drive.ParamDef{
			Name:        "username",
			Type:        "string",
			Required:    true,
			Description: "WebDAV authentication username",
			Example:     "user",
		},
		drive.ParamDef{
			Name:        "password",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "WebDAV authentication password or app token",
			Example:     "your-password-or-app-token",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Optional path under the WebDAV base URL used as this mount root",
			Default:     "/",
			Example:     "/qrypt",
		},
	)
}

// New creates a new WebDAV driver.
func New(opts Options) *Driver {
	baseURL := webdavBaseURL(opts.URL, opts.RootPath)
	return &Driver{
		baseURL:  baseURL,
		rootPath: opts.RootPath,
		username: opts.Username,
		password: opts.Password,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:       20,
				IdleConnTimeout:    30 * time.Second,
				DisableCompression: false,
			},
		},
	}
}

func webdavBaseURL(rawURL, rootPath string) string {
	baseURL := rawURL
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	rootPath = strings.Trim(rootPath, "/")
	if rootPath == "" || rootPath == "." {
		return baseURL
	}
	return baseURL + escapePath(rootPath) + "/"
}

// ─── drive.Driver interface ──────────────────────────────────────────────

func (d *Driver) Init(ctx context.Context) error {
	// Verify the connection by PROPFIND on root.
	if _, err := d.propfind(ctx, d.baseURL, 0); err != nil {
		if d.rootPath != "" && d.rootPath != "/" {
			return fmt.Errorf("webdav: init root_path %q: %w", d.rootPath, err)
		}
		return fmt.Errorf("webdav: init: %w", err)
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error { return nil }

// List returns the children of the directory identified by parentID.
// parentID is a path like "/" (root) or "/subdir".
func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	urlStr := d.resolveURL(parentID)
	responses, err := d.propfind(ctx, urlStr, 1)
	if err != nil {
		return nil, fmt.Errorf("webdav: list: %w", err)
	}

	parentRel := d.relativePath(parentID)
	entries := make([]drive.Entry, 0, len(responses))
	for _, r := range responses {
		rPath := d.toPath(r.Href)
		if rPath == parentRel {
			continue // skip the directory itself
		}
		name := path.Base(rPath)
		if name == "" || name == "." || name == ".." {
			continue
		}

		isDir, size, modTime := d.parseProps(r.Propstat)
		entries = append(entries, drive.Entry{
			ID:       rPath,
			ParentID: parentRel,
			Name:     name,
			IsDir:    isDir,
			Size:     size,
			ModTime:  modTime,
		})
	}
	return entries, nil
}

// Read reads a portion of a file.
//
// If size > 0, it reads exactly size bytes starting at offset.
// If size == 0, it reads from offset to EOF — matching the convention
// used by the VFS layer and localfs/osutil.OpenRead.
func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if entry.Size > 0 && offset >= entry.Size {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if size > 0 && entry.Size > 0 && offset+size > entry.Size {
		size = entry.Size - offset
	}
	urlStr := d.resolveURL(entry.ID)
	req, err := d.newRequest(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	switch {
	case size > 0:
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))
	case offset > 0:
		// size == 0 && offset > 0 — rest-of-file from offset.
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("webdav: read: %w", err)
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		resp.Body.Close()
		if entry.Size > 0 && offset >= entry.Size {
			return io.NopCloser(bytes.NewReader(nil)), nil
		}
		return nil, fmt.Errorf("webdav: read: unexpected status %d for %s", resp.StatusCode, entry.ID)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("webdav: read: unexpected status %d for %s", resp.StatusCode, entry.ID)
	}
	return resp.Body, nil
}

// ─── drive.Writer interface ──────────────────────────────────────────────

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	destURL := d.childURL(parentID, name)
	req, err := d.newRequest(ctx, "MKCOL", destURL, nil)
	if err != nil {
		return drive.Entry{}, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("webdav: mkdir: %w", err)
	}
	resp.Body.Close()
	// 201 Created, 204 No Content, 405 Method Not Allowed (already exists)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return drive.Entry{}, fmt.Errorf("webdav: mkdir: status %d for %s/%s", resp.StatusCode, parentID, name)
	}
	childPath := d.joinPath(parentID, name)
	return drive.Entry{
		ID:       childPath,
		ParentID: d.relativePath(parentID),
		Name:     name,
		IsDir:    true,
		ModTime:  time.Now(),
	}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	srcURL := d.resolveURL(entry.ID)
	destURL := d.childURL(dstParentID, entry.Name)
	return d.move(ctx, srcURL, destURL)
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	srcURL := d.resolveURL(entry.ID)
	destURL := d.parentURL(srcURL) + url.PathEscape(newName)
	return d.move(ctx, srcURL, destURL)
}

func (d *Driver) HealthCheck(ctx context.Context) drive.HealthStatus {
	start := time.Now()
	status := drive.HealthStatus{Driver: "webdav", CheckedAt: start}
	_, err := d.propfind(ctx, d.baseURL, 0)
	status.Latency = time.Since(start).String()
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.OK = true
	return status
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	urlStr := d.resolveURL(entry.ID)
	req, err := d.newRequest(ctx, http.MethodDelete, urlStr, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("webdav: remove: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("webdav: remove: status %d for %s", resp.StatusCode, entry.ID)
	}
	return nil
}

// ─── drive.Uploader interface ────────────────────────────────────────────

func (d *Driver) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (drive.Entry, error) {
	destURL := d.childURL(parentID, name)
	req, err := d.newRequest(ctx, http.MethodPut, destURL, body)
	if err != nil {
		return drive.Entry{}, err
	}
	if size > 0 {
		req.ContentLength = size
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("webdav: put: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return drive.Entry{}, fmt.Errorf("webdav: put: status %d for %s/%s", resp.StatusCode, parentID, name)
	}
	childPath := d.joinPath(parentID, name)
	return drive.Entry{
		ID:       childPath,
		ParentID: d.relativePath(parentID),
		Name:     name,
		Size:     size,
		ModTime:  time.Now(),
	}, nil
}

// ─── internal helpers ────────────────────────────────────────────────────

// childURL returns the full URL for a child resource in a parent directory.
// Both parentID and name are unescaped qrypt paths; name gets URL-escaped
// to handle special characters (#, ?, %, etc.).
func (d *Driver) childURL(parentID, name string) string {
	parent := d.resolveURL(parentID)
	if !strings.HasSuffix(parent, "/") {
		parent += "/"
	}
	return parent + url.PathEscape(name)
}

// resolveURL converts a qrypt path ID to a full WebDAV URL.
// Each path segment is URL-escaped to handle reserved characters.
func (d *Driver) resolveURL(id string) string {
	if id == "" || id == "/" || id == "0" {
		return d.baseURL
	}
	clean := strings.TrimPrefix(id, "/")
	return d.baseURL + escapePath(clean)
}

// escapePath URL-escapes each segment of a slash-separated path.
func escapePath(p string) string {
	if p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if part != "" {
			parts[i] = url.PathEscape(part)
		}
	}
	return strings.Join(parts, "/")
}

// relativePath normalises a qrypt path ID for internal use.
func (d *Driver) relativePath(id string) string {
	if id == "" || id == "/" || id == "0" {
		return "/"
	}
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	return path.Clean(id)
}

// joinPath joins a parent path and child name, normalising the result.
func (d *Driver) joinPath(parentID, name string) string {
	p := d.relativePath(parentID)
	return path.Clean(p + "/" + name)
}

// parentURL returns the WebDAV URL of the parent of the given resource URL.
func (d *Driver) parentURL(resourceURL string) string {
	if !strings.HasSuffix(resourceURL, "/") {
		resourceURL += "/"
	}
	// Strip the last path segment.
	trimmed := strings.TrimSuffix(resourceURL, "/")
	parent := trimmed[:strings.LastIndex(trimmed, "/")+1]
	return parent
}

// toPath converts a PROPFIND href to a qrypt path (e.g. "/folder/file").
func (d *Driver) toPath(href string) string {
	// Some servers return absolute URLs, some return relative paths.
	// Parse absolute URLs before unescaping so encoded '#' and '?' path bytes
	// do not become URL fragments or queries.
	pathPart := href
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		if u, err := url.Parse(href); err == nil {
			pathPart = u.EscapedPath()
		}
	}
	decoded, err := url.PathUnescape(pathPart)
	if err != nil {
		decoded = pathPart
	}

	// Strip the base URL's path prefix.
	basePath := d.baseURLPath()
	if basePath == "" || basePath == "/" {
		return path.Clean("/" + decoded)
	}
	if decoded == basePath || decoded == basePath+"/" {
		return "/"
	}
	if strings.HasPrefix(decoded, basePath+"/") {
		decoded = strings.TrimPrefix(decoded, basePath)
	}
	return path.Clean("/" + decoded)
}

// baseURLPath returns the path component of the base URL.
func (d *Driver) baseURLPath() string {
	if u, err := url.Parse(d.baseURL); err == nil {
		p := strings.TrimSuffix(u.Path, "/")
		if p == "" {
			p = "/"
		}
		return p
	}
	return "/"
}

// propfind sends a PROPFIND request at the given depth.
func (d *Driver) propfind(ctx context.Context, urlStr string, depth int) ([]propfindResponse, error) {
	req, err := d.newRequest(ctx, "PROPFIND", urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", fmt.Sprintf("%d", depth))
	req.Header.Set("Content-Type", "application/xml")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webdav: propfind: unexpected status %d for %s", resp.StatusCode, urlStr)
	}

	var ms multistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("webdav: propfind: decode: %w", err)
	}
	return ms.Responses, nil
}

// parseProps extracts isDir, size, and modTime from a propstat slice.
func (d *Driver) parseProps(propstats []propstat) (bool, int64, time.Time) {
	var isDir bool
	var size int64
	var modTime time.Time
	for _, ps := range propstats {
		if ps.Prop.ResourceType != nil && ps.Prop.ResourceType.Collection != nil {
			isDir = true
		}
		if ps.Prop.GetContentLen != "" {
			fmt.Sscanf(ps.Prop.GetContentLen, "%d", &size)
		}
		if ps.Prop.GetLastMod != "" {
			modTime = parseWebDAVTime(ps.Prop.GetLastMod)
		}
	}
	return isDir, size, modTime
}

// move sends a MOVE request from srcURL to destURL.
func (d *Driver) move(ctx context.Context, srcURL, destURL string) error {
	req, err := d.newRequest(ctx, "MOVE", srcURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Destination", destURL)
	req.Header.Set("Overwrite", "F")
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("webdav: move: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("webdav: move: status %d for %s", resp.StatusCode, srcURL)
	}
	return nil
}

// newRequest creates an http.Request with Basic Auth set.
func (d *Driver) newRequest(ctx context.Context, method, urlStr string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, err
	}
	if d.username != "" {
		req.SetBasicAuth(d.username, d.password)
	}
	return req, nil
}

// ─── time parsing ────────────────────────────────────────────────────────

// WebDAV uses RFC 1123 / RFC 2822 dates.
var webdavTimeFormats = []string{
	time.RFC1123,
	time.RFC1123Z,
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05-07:00",
}

func parseWebDAVTime(s string) time.Time {
	for _, fmt := range webdavTimeFormats {
		if t, err := time.Parse(fmt, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ─── interface guards ────────────────────────────────────────────────────

var _ drive.Driver = (*Driver)(nil)
var _ drive.Writer = (*Driver)(nil)
var _ drive.Uploader = (*Driver)(nil)
