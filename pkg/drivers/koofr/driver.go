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
		{Name: "mount_id", Type: "string", Scope: drive.ParamScopeMount, Description: "Lock the mount to one provider mount id; by default the root aggregates the primary storage and online external mounts", DescriptionI18n: map[string]string{"zh-CN": "锁定单一网盘挂载 ID；默认挂载根聚合主存储与在线外部挂载"}, Example: "749b91a4-c96b-4cfb-a457-526827ad9622"},
		{Name: "root_path", Type: "string", Scope: drive.ParamScopeMount, Description: "Directory path used as this mount root; requires mount_id (the default virtual root only serves /)", DescriptionI18n: map[string]string{"zh-CN": "作为挂载根的目录路径；需配合 mount_id 使用（默认虚拟根仅支持 /）"}, Default: "/", Example: "/qrypt"},
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
//
// When mount_id is omitted the driver serves a virtual root instead: the
// primary storage root plus one root-level directory per online external
// mount. Entries on an external mount carry "ext:<mountId>"-prefixed ids and
// every operation routes to the owning mount.
type Driver struct {
	drive.UnsupportedOperations

	cl        *client
	mountID   string
	mountName string
	rootPath  string
	spaceMiB  atomic.Int64
	uploads   atomic.Int64

	// locked is true when mount_id was configured explicitly: the driver
	// then serves exactly that mount and root_path applies to it.
	locked bool
	// extMounts caches the online non-primary mounts discovered at Init
	// (virtual root mode only). Runtime mount-list changes are not detected.
	extMounts []apiMount

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
		locked:   strings.TrimSpace(opts.MountID) != "",
		rootPath: rootPath,
	}
}

// virtualRoot reports whether the driver serves the aggregated account root
// (mount_id omitted) instead of one locked provider mount.
func (d *Driver) virtualRoot() bool { return !d.locked }

// Init validates credentials, resolves the provider mount(s) and root path,
// and commits them only after every check succeeds so a retried Init never
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
	rootPath, err := cleanProviderPath(d.rootPath)
	if err != nil {
		return fmt.Errorf("koofr: invalid root_path %q: %w", d.rootPath, err)
	}
	if d.locked {
		return d.initLocked(ctx, mounts.Mounts, rootPath)
	}
	return d.initVirtualRoot(ctx, mounts.Mounts, rootPath)
}

// initLocked validates and commits an explicitly configured mount_id.
func (d *Driver) initLocked(ctx context.Context, mounts []apiMount, rootPath string) error {
	mount := d.selectMount(mounts)
	if mount == nil {
		err := fmt.Errorf("koofr: mount %q not found for this account: %w", d.mountID, drive.ErrInvalidInput)
		d.setLastError(err)
		return err
	}
	if err := checkMountUsable(mount); err != nil {
		d.setLastError(err)
		return err
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

// initVirtualRoot validates and commits the default aggregated root: the
// primary storage plus every online external mount as a root-level
// directory. root_path must stay "/" — subdirectory roots require an
// explicit mount_id — and an external mount whose name collides with a
// primary root entry (or another external mount) fails initialization.
func (d *Driver) initVirtualRoot(ctx context.Context, mounts []apiMount, rootPath string) error {
	var primary *apiMount
	for i := range mounts {
		if mounts[i].IsPrimary {
			primary = &mounts[i]
			break
		}
	}
	if primary == nil {
		err := fmt.Errorf("koofr: account has no primary mount: %w", drive.ErrInvalidInput)
		d.setLastError(err)
		return err
	}
	if err := checkMountUsable(primary); err != nil {
		d.setLastError(err)
		return err
	}
	if rootPath != "/" {
		err := fmt.Errorf("koofr: root_path %q requires an explicit mount_id; the default virtual root only serves the account root: %w", rootPath, drive.ErrInvalidInput)
		d.setLastError(err)
		return err
	}
	var externals []apiMount
	for i := range mounts {
		if mounts[i].IsPrimary || !mounts[i].Online {
			continue
		}
		externals = append(externals, mounts[i])
	}
	rootEntries, err := d.listDirOn(ctx, primary.ID, "/", "/")
	if err != nil {
		err = fmt.Errorf("koofr: list primary root: %w", err)
		d.setLastError(err)
		return err
	}
	taken := map[string]string{}
	for _, entry := range rootEntries {
		taken[lowerASCII(entry.Name)] = "primary storage entry " + entry.Name
	}
	for _, ext := range externals {
		key := lowerASCII(ext.Name)
		if holder, ok := taken[key]; ok {
			err := fmt.Errorf("koofr: external mount %q (id %s) conflicts with a %s in the virtual root; rename one of them or configure mount_id explicitly: %w", ext.Name, ext.ID, holder, drive.ErrInvalidInput)
			d.setLastError(err)
			return err
		}
		taken[key] = "external mount " + ext.Name
	}
	d.mountID = primary.ID
	d.mountName = primary.Name
	d.rootPath = "/"
	d.extMounts = externals
	d.spaceMiB.Store(primary.SpaceTotal - primary.SpaceUsed)
	return nil
}

// checkMountUsable rejects offline and read-only mounts for the mount the
// driver is permanently rooted at (the locked mount or the primary).
func checkMountUsable(mount *apiMount) error {
	if !mount.Online {
		return fmt.Errorf("koofr: mount %q (%s) is offline: %w", mount.ID, mount.Name, drive.ErrInvalidInput)
	}
	if !mount.CanWrite || !mount.CanUpload {
		return fmt.Errorf("koofr: mount %q (%s) is read-only: %w", mount.ID, mount.Name, drive.ErrPermission)
	}
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
// the configured root. In virtual-root mode a first segment naming an
// external mount routes the rest of the path onto that mount and returns an
// "ext:" id.
func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	clean, err := cleanMountPath(p)
	if err != nil {
		return "", err
	}
	if d.virtualRoot() && clean != "/" {
		trimmed := strings.TrimPrefix(clean, "/")
		first := clean
		rest := ""
		if i := strings.IndexByte(trimmed, '/'); i >= 0 {
			first = "/" + trimmed[:i]
			rest = trimmed[i:]
		}
		if mount, ok := d.extMountByName(strings.TrimPrefix(first, "/")); ok {
			full := "/"
			if rest != "" {
				full = rest
			}
			t := opTarget{mount: mount, external: true}
			if _, err := d.infoOn(ctx, t, full); err != nil {
				return "", err
			}
			return d.idPath(t, full), nil
		}
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

// resolveWithin maps a caller-facing id to the absolute provider path and
// refuses ids that fall outside root_path. Entry ids normally come from
// listings and are in-bounds by construction; the guard exists so a caller
// passing a raw provider path (or a stale id from another root) cannot aim
// an operation outside the mounted subtree.
func (d *Driver) resolveWithin(id, op string) (string, error) {
	path := d.resolveID(id)
	if len(path) == 0 || path[0] != '/' || !isWithin(path, d.rootPath) {
		return "", fmt.Errorf("koofr: %s: id %q escapes root_path %q: %w", op, id, d.rootPath, drive.ErrInvalidInput)
	}
	return path, nil
}

// extIDPrefix marks caller-facing ids that live on an external mount:
// "ext:<mountId>" is the mount's root directory and "ext:<mountId>/sub/file"
// a nested entry. Primary-storage ids stay absolute provider paths, which
// never start with "ext:", so the two spaces cannot collide.
const extIDPrefix = "ext:"

func extRootID(mountID string) string { return extIDPrefix + mountID }

// splitExtID decodes an external-mount id into its mount id and the absolute
// path on that mount. ok is false for primary-storage path ids.
func splitExtID(id string) (mountID, path string, ok bool) {
	if !strings.HasPrefix(id, extIDPrefix) {
		return "", "", false
	}
	rest := id[len(extIDPrefix):]
	if rest == "" {
		return "", "", false
	}
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		if slash == 0 {
			return "", "", false
		}
		return rest[:slash], rest[slash:], true
	}
	return rest, "/", true
}

// opTarget is one resolved operation target: the provider mount that owns
// the entry and the absolute path on that mount.
type opTarget struct {
	mount    apiMount
	path     string
	external bool
}

// resolveTarget routes a caller-facing id to its owning mount and path. In
// locked mode this is exactly resolveWithin on the configured mount; in
// virtual-root mode "ext:"-prefixed ids address the cached external mounts.
func (d *Driver) resolveTarget(id, op string) (opTarget, error) {
	if d.locked {
		path, err := d.resolveWithin(id, op)
		if err != nil {
			return opTarget{}, err
		}
		return opTarget{mount: apiMount{ID: d.mountID}, path: path}, nil
	}
	trimmed := strings.TrimSpace(id)
	if mountID, path, ok := splitExtID(trimmed); ok {
		mount, known := d.extMountByID(mountID)
		if !known {
			return opTarget{}, fmt.Errorf("koofr: %s: id %q names an external mount that is unknown or offline on this mount: %w", op, id, drive.ErrInvalidInput)
		}
		return opTarget{mount: mount, path: path, external: true}, nil
	}
	if trimmed == "" || trimmed == "/" {
		return opTarget{mount: apiMount{ID: d.mountID}, path: d.rootPath}, nil
	}
	if trimmed[0] != '/' {
		return opTarget{}, fmt.Errorf("koofr: %s: id %q is not an absolute path: %w", op, id, drive.ErrInvalidInput)
	}
	return opTarget{mount: apiMount{ID: d.mountID}, path: trimmed}, nil
}

// extMountByID looks a cached external mount up by provider id.
func (d *Driver) extMountByID(mountID string) (apiMount, bool) {
	for _, mount := range d.extMounts {
		if mount.ID == mountID {
			return mount, true
		}
	}
	return apiMount{}, false
}

// extMountByName fold-matches a root-level directory name against the cached
// external mounts (provider names are case-insensitive).
func (d *Driver) extMountByName(name string) (apiMount, bool) {
	lower := lowerASCII(name)
	for _, mount := range d.extMounts {
		if lowerASCII(mount.Name) == lower {
			return mount, true
		}
	}
	return apiMount{}, false
}

// idPath maps an absolute provider path on a target's mount back to the
// caller-facing id space: paths on the primary storage are their own ids,
// external-mount paths carry the "ext:<mountId>" prefix.
func (d *Driver) idPath(t opTarget, path string) string {
	if !t.external {
		return path
	}
	if path == "/" {
		return extRootID(t.mount.ID)
	}
	return extIDPrefix + t.mount.ID + path
}

// checkWritable rejects writes aimed at a read-only external mount before
// any request is issued.
func (t opTarget) checkWritable(op string, upload bool) error {
	if !t.external {
		return nil
	}
	if !t.mount.CanWrite || (upload && !t.mount.CanUpload) {
		return fmt.Errorf("koofr: %s: external mount %q is read-only: %w", op, t.mount.Name, drive.ErrPermission)
	}
	return nil
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
