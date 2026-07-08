// Package p115 implements the 115 cloud drive driver.
//
// STATUS: BLOCKED by 115's Alibaba Cloud WAF. The API endpoints
// (webapi.115.com, aps.115.com) return HTTP 405 for any non-browser
// request regardless of authentication state. Bypassing the WAF would
// require a real browser engine (e.g. Chromedp) or a different API
// approach. The code is kept for reference in case 115 changes their
// WAF policy or an alternative API endpoint becomes available.
package p115

import (
	"context"
	"fmt"
	"io"
	"strings"

	"golang.org/x/time/rate"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const defaultAppVer = "35.6.0.3"

var appVer = defaultAppVer

type Driver struct {
	cl        *driver115.Pan115Client
	rootID    string
	rootPath  string
	cookies   string
	limitRate float64
	limiter   *rate.Limiter
}

func init() {
	drive.Register("115", func(params drive.Params) (drive.Driver, error) {
		cookie := params["cookie"]
		if cookie == "" {
			return nil, fmt.Errorf("115: missing cookie")
		}
		return New(Options{
			Cookie:    cookie,
			RootPath:  params["root_path"],
			LimitRate: 2,
		}), nil
	},
		drive.ParamDef{
			Name:        "cookie",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "115 cloud drive authentication cookie",
			Example:     "k1=v1; k2=v2",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Virtual root path, resolved to the provider folder ID at startup",
			Default:     "/",
			Example:     "/qrypt",
		},
	)
}

type Options struct {
	Cookie    string
	RootID    string
	RootPath  string
	LimitRate float64
}

func New(opts Options) *Driver {
	return &Driver{
		rootID:    opts.RootID,
		rootPath:  opts.RootPath,
		cookies:   opts.Cookie,
		limitRate: opts.LimitRate,
	}
}

func (d *Driver) Init(ctx context.Context) error {
	if d.cookies == "" {
		return fmt.Errorf("115: Init: missing cookie")
	}
	if d.limitRate > 0 {
		d.limiter = rate.NewLimiter(rate.Limit(d.limitRate), 1)
	}
	d.cl = driver115.New(
		driver115.UA(fmt.Sprintf("Mozilla/5.0 115Browser/%s", appVer)),
	)
	allCookies := cookieMap(d.cookies)
	cred := &driver115.Credential{}
	if err := cred.FromCookie(d.cookies); err == nil {
		d.cl.ImportCredential(cred)
	}
	d.cl.ImportCookies(allCookies)
	if err := d.cl.LoginCheck(); err != nil {
		return err
	}
	if d.rootID == "" {
		d.rootID = "0"
	}
	if d.rootPath != "" && d.rootPath != "/" {
		rootID, err := d.resolvePathFrom(ctx, d.rootID, d.rootPath)
		if err != nil {
			return fmt.Errorf("115: resolve root_path %q: %w", d.rootPath, err)
		}
		d.rootID = rootID
	}
	return nil
}

func (d *Driver) Drop(context.Context) error {
	return nil
}

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	if err := d.waitLimit(ctx); err != nil {
		return nil, err
	}
	return d.getFiles(d.resolveID(parentID))
}

func (d *Driver) Read(ctx context.Context, e drive.Entry, offset, size int64) (io.ReadCloser, error) {
	_ = d.waitLimit(ctx)
	return nil, fmt.Errorf("115: Read not implemented (WAF blocked)")
}

func (d *Driver) waitLimit(ctx context.Context) error {
	if d.limiter != nil {
		return d.limiter.Wait(ctx)
	}
	return nil
}

func (d *Driver) getFiles(dirID string) ([]drive.Entry, error) {
	files, err := d.cl.ListWithLimit(dirID, 1000, driver115.WithMultiUrls())
	if err != nil {
		return nil, err
	}
	entries := make([]drive.Entry, len(*files))
	for i, f := range *files {
		entries[i] = drive.Entry{
			ID:      f.GetID(),
			Name:    f.GetName(),
			Size:    f.GetSize(),
			IsDir:   f.IsDir(),
			ModTime: f.ModTime(),
			Extra:   f,
		}
	}
	return entries, nil
}

func (d *Driver) resolveID(fileID string) string {
	if fileID == "" || fileID == "0" || fileID == "/" {
		return d.rootID
	}
	return fileID
}

func (d *Driver) resolvePathFrom(ctx context.Context, rootID, p string) (string, error) {
	currentID := d.resolveID(rootID)
	p = strings.Trim(p, "/")
	if p == "" {
		return currentID, nil
	}
	for _, segment := range strings.Split(p, "/") {
		entries, err := d.List(ctx, currentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.IsDir && entry.Name == segment {
				currentID = entry.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("directory %q not found under %q", segment, p)
		}
	}
	return currentID, nil
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

var _ drive.Driver = (*Driver)(nil)
