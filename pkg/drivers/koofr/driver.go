// Package koofr implements a qrypt driver for Koofr (and compatible services
// such as Digi Storage) over the app.koofr.net REST API with app-password
// Basic authentication.
package koofr

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const defaultAPIBase = "https://app.koofr.net"

func koofrParams() []drive.ParamDef {
	return []drive.ParamDef{
		{Name: "email", Type: "string", Required: true, Scope: drive.ParamScopeCredential, Description: "Koofr account email", DescriptionI18n: map[string]string{"zh-CN": "Koofr 账号邮箱"}, Example: "user@example.com"},
		{Name: "app_password", Type: "string", Required: true, Secret: true, Scope: drive.ParamScopeCredential, Description: "Koofr app password (Settings -> App passwords)", DescriptionI18n: map[string]string{"zh-CN": "Koofr 应用密码（设置 -> 应用密码）"}, Example: "your-app-password"},
		{Name: "mount_id", Type: "string", Scope: drive.ParamScopeMount, Description: "Provider mount id; defaults to the primary mount", DescriptionI18n: map[string]string{"zh-CN": "网盘挂载 ID；默认使用主存储"}, Example: "749b91a4-c96b-4cfb-a457-526827ad9622"},
		{Name: "root_path", Type: "string", Scope: drive.ParamScopeMount, Description: "Directory path used as this mount root", DescriptionI18n: map[string]string{"zh-CN": "作为挂载根的目录路径"}, Default: "/", Example: "/qrypt"},
		{Name: "api_base_url", Type: "string", Scope: drive.ParamScopeBoth, Description: "Custom Koofr API base URL", DescriptionI18n: map[string]string{"zh-CN": "自定义 Koofr API 基础 URL"}, Default: defaultAPIBase, Example: defaultAPIBase},
	}
}

func init() {
	drive.Register("koofr", func(params drive.Params) (drive.Driver, error) {
		email := strings.TrimSpace(params["email"])
		if email == "" {
			return nil, fmt.Errorf("koofr: missing email")
		}
		password := params["app_password"]
		if strings.TrimSpace(password) == "" {
			return nil, fmt.Errorf("koofr: missing app_password")
		}
		return New(Options{
			Email:      email,
			Password:   password,
			MountID:    strings.TrimSpace(params["mount_id"]),
			RootPath:   params["root_path"],
			APIBaseURL: params["api_base_url"],
		}), nil
	}, koofrParams()...)
}

// Options configures a Koofr driver.
type Options struct {
	Email      string
	Password   string
	MountID    string
	RootPath   string
	APIBaseURL string
}

// Driver talks to one Koofr provider mount. Entry ids are absolute provider
// paths ("/dir/file"); the empty id and "/" resolve to the configured root.
type Driver struct {
	drive.UnsupportedOperations

	cl        *client
	mountID   string
	mountName string
	rootPath  string
	spaceMiB  atomic.Int64
	uploads   atomic.Int64

	lastErrMu sync.Mutex
	lastErr   string
}

// New constructs a Koofr driver. Init must run before any other call.
func New(opts Options) *Driver {
	rootPath := strings.TrimSpace(opts.RootPath)
	if rootPath == "" {
		rootPath = "/"
	}
	return &Driver{
		cl:       newClient(opts),
		mountID:  strings.TrimSpace(opts.MountID),
		rootPath: rootPath,
	}
}

// Init validates credentials, resolves the provider mount and root path, and
// commits them only after every check succeeds so a retried Init never
// silently mounts the account root.
func (d *Driver) Init(ctx context.Context) error {
	var user apiUser
	if _, err := d.cl.get(ctx, "/user", nil, &user); err != nil {
		err = fmt.Errorf("koofr: validate credentials: %w", err)
		d.setLastError(err)
		return err
	}
	var mounts apiMountList
	if _, err := d.cl.get(ctx, "/mounts", nil, &mounts); err != nil {
		err = fmt.Errorf("koofr: list mounts: %w", err)
		d.setLastError(err)
		return err
	}
	mount := d.selectMount(mounts.Mounts)
	if mount == nil {
		err := fmt.Errorf("koofr: mount %q not found for this account: %w", d.mountID, drive.ErrInvalidInput)
		d.setLastError(err)
		return err
	}
	if !mount.Online {
		err := fmt.Errorf("koofr: mount %q (%s) is offline: %w", mount.ID, mount.Name, drive.ErrInvalidInput)
		d.setLastError(err)
		return err
	}
	if !mount.CanWrite || !mount.CanUpload {
		err := fmt.Errorf("koofr: mount %q (%s) is read-only: %w", mount.ID, mount.Name, drive.ErrPermission)
		d.setLastError(err)
		return err
	}
	rootPath, err := cleanProviderPath(d.rootPath)
	if err != nil {
		return fmt.Errorf("koofr: invalid root_path %q: %w", d.rootPath, err)
	}
	if rootPath != "/" {
		info, err := d.info(ctx, rootPath)
		if err != nil {
			err = fmt.Errorf("koofr: resolve root_path %q: %w", rootPath, err)
			d.setLastError(err)
			return err
		}
		if !info.IsDir {
			return fmt.Errorf("koofr: root_path %q is not a directory: %w", rootPath, drive.ErrInvalidInput)
		}
	}
	d.mountID = mount.ID
	d.mountName = mount.Name
	d.rootPath = rootPath
	d.spaceMiB.Store(mount.SpaceTotal - mount.SpaceUsed)
	return nil
}

func (d *Driver) selectMount(mounts []apiMount) *apiMount {
	if d.mountID != "" {
		for i := range mounts {
			if mounts[i].ID == d.mountID {
				return &mounts[i]
			}
		}
		return nil
	}
	for i := range mounts {
		if mounts[i].IsPrimary {
			return &mounts[i]
		}
	}
	if len(mounts) > 0 {
		return &mounts[0]
	}
	return nil
}

func (d *Driver) Drop(context.Context) error {
	d.cl.http.CloseIdleConnections()
	return nil
}

// Space reports provider capacity in bytes; Koofr reports MiB figures.
func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	var mount apiMount
	if _, err := d.cl.get(ctx, "/mounts/"+url.PathEscape(d.mountID), nil, &mount); err != nil {
		err = fmt.Errorf("koofr: space: %w", err)
		d.setLastError(err)
		return drive.Space{}, err
	}
	total := mount.SpaceTotal << 20
	free := (mount.SpaceTotal - mount.SpaceUsed) << 20
	if free < 0 {
		free = 0
	}
	d.spaceMiB.Store(mount.SpaceTotal - mount.SpaceUsed)
	return drive.Space{Total: total, Free: free}, nil
}

// ResolvePath walks a mount-relative path segment by segment and returns the
// absolute provider path id. The path is resolved against root_path, so a
// mount rooted at /backup resolves /data to /backup/data and never outside
// the configured root.
func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	clean, err := cleanMountPath(p)
	if err != nil {
		return "", err
	}
	full := d.rootPath
	if clean != "/" {
		full = joinPath(d.rootPath, strings.TrimPrefix(clean, "/"))
	}
	if !isWithin(full, d.rootPath) {
		return "", fmt.Errorf("koofr: path %q escapes root_path %q: %w", p, d.rootPath, drive.ErrInvalidInput)
	}
	// The provider treats an empty path as the mount root, so every lookup
	// uses the absolute path and verifies the final entry exists.
	info, err := d.info(ctx, full)
	if err != nil {
		return "", err
	}
	_ = info
	return full, nil
}

// resolveID maps a caller-facing id to the absolute provider path. The empty
// id and "/" are the mount root.
func (d *Driver) resolveID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || id == "/" {
		return d.rootPath
	}
	return id
}

// cleanMountPath normalizes a mount-relative path. It rejects ".." escapes
// and returns "/" for the root.
func cleanMountPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" || p == "." {
		return "/", nil
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("koofr: path escapes mount root: %w", drive.ErrInvalidInput)
		}
		if err := validateName(part); err != nil {
			return "", err
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return "/", nil
	}
	return "/" + strings.Join(out, "/"), nil
}

// cleanProviderPath normalizes a provider-absolute path used as root_path.
func cleanProviderPath(p string) (string, error) {
	return cleanMountPath(p)
}

// joinPath appends name to an absolute parent path.
func joinPath(parent, name string) string {
	if parent == "" || parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

// parentPath returns the parent of an absolute path, "/" at the top.
func parentPath(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

func (d *Driver) setLastError(err error) {
	if err == nil {
		return
	}
	d.lastErrMu.Lock()
	d.lastErr = drive.Snippet([]byte(err.Error()))
	d.lastErrMu.Unlock()
}

func (d *Driver) getLastError() string {
	d.lastErrMu.Lock()
	defer d.lastErrMu.Unlock()
	return d.lastErr
}

var _ drive.Driver = (*Driver)(nil)
