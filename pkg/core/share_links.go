package core

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Core-level share-link request problems. They classify a request rejected
// before any provider call; the CLI maps the input ones to a usage exit code.
var (
	// ErrSharePathsRequired means a create request named no path.
	ErrSharePathsRequired = errors.New("core: share requires at least one path")
	// ErrSharePathNotFound means a share path does not exist.
	ErrSharePathNotFound = errors.New("core: share path not found")
	// ErrShareRootPath means a caller tried to share a mount root, which is not
	// an entry a provider can publish.
	ErrShareRootPath = errors.New("core: cannot share a mount root")
	// ErrShareCrossMount means one request named paths on different mounts.
	ErrShareCrossMount = errors.New("core: share paths must be on one mount")
	// ErrShareMountRequired means no acting mount could be chosen because
	// several mounts are configured and none was named.
	ErrShareMountRequired = errors.New("core: share mount is required")
	// ErrShareMountNotFound means the named mount is not configured.
	ErrShareMountNotFound = errors.New("core: share mount not found")
	// ErrShareMountUnavailable means the mount failed to initialize.
	ErrShareMountUnavailable = errors.New("core: share mount is unavailable")
	// ErrShareUnsupported means the acting driver cannot perform the share
	// operation or does not declare the option.
	ErrShareUnsupported = errors.New("core: share is unsupported")
	// ErrShareEncryptedDestination means a receive cloud destination applies
	// qrypt encryption. A provider-side copy never passes through qrypt, so the
	// bytes cannot honour that mount's ciphertext convention. A local download
	// destination inside an encrypted mount stays allowed.
	ErrShareEncryptedDestination = errors.New("core: receive destination cannot be an encrypted mount")
)

// defaultShareLinkTimeout bounds a synchronous link operation. A provider that
// queues the work longer than this surfaces as share_timeout instead of
// blocking the caller forever.
const defaultShareLinkTimeout = 30 * time.Second

// shareSurface is the acting mount plus the provider share contract it exposes.
type shareSurface struct {
	mount   vfs.MountInfo
	manager drive.ShareManager
	lister  drive.ShareLister
	policy  drive.SharePolicy
}

// CreateShareLink publishes a link synchronously and returns it. The provider's
// own asynchronous create task is handled inside the driver; the Core only
// bounds the whole call.
func (c *Core) CreateShareLink(ctx context.Context, req ShareLinkCreateRequest) (drive.Share, error) {
	entries, mount, err := c.resolveShareCreateEntries(ctx, req.Paths, req.Mount)
	if err != nil {
		return drive.Share{}, err
	}
	surface, err := c.resolveShareSurface(mount)
	if err != nil {
		return drive.Share{}, err
	}

	passcode := strings.TrimSpace(req.Passcode)
	if req.GeneratePasscode && passcode == "" {
		if surface.policy.Passcode == drive.PolicyUnsupported {
			return drive.Share{}, fmt.Errorf("%w: mount %q does not support passcodes", ErrShareUnsupported, surface.mount.Name)
		}
		generated, err := GenerateSharePasscode()
		if err != nil {
			return drive.Share{}, err
		}
		passcode = generated
	}

	create := drive.ShareCreateRequest{
		Entries:    entries,
		Title:      strings.TrimSpace(req.Title),
		Expiry:     req.Expiry,
		Passcode:   passcode,
		VisitLimit: req.VisitLimit,
	}
	if err := surface.policy.ValidateCreateRequest(create); err != nil {
		return drive.Share{}, shareOptionError(err)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.shareLinkTimeout())
	defer cancel()
	share, err := surface.manager.CreateShare(callCtx, create)
	if err != nil {
		return drive.Share{}, shareCallError(callCtx, err)
	}
	if share.LinkID == "" && share.URL == "" {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "provider returned no link")
	}
	share.NativeID = strings.TrimSpace(share.NativeID)
	// The owner always sees the passcode that protects the link, even when the
	// provider does not echo it back.
	if share.Passcode == "" {
		share.Passcode = passcode
	}
	return share, nil
}

// ListShareLinks pages through the acting mount's links.
func (c *Core) ListShareLinks(ctx context.Context, req ShareLinkListRequest) (drive.SharePage, error) {
	surface, err := c.resolveShareSurface(req.Mount)
	if err != nil {
		return drive.SharePage{}, err
	}
	if err := surface.policy.ValidateListRequest(drive.ShareListRequest{}); err != nil {
		return drive.SharePage{}, shareOptionError(err)
	}
	if surface.lister == nil {
		return drive.SharePage{}, shareOptionError(drive.NewShareError(drive.ShareErrUnsupported, "", "this provider does not list links"))
	}
	callCtx, cancel := context.WithTimeout(ctx, c.shareLinkTimeout())
	defer cancel()
	page, err := surface.lister.ListShares(callCtx, drive.ShareListRequest{
		Cursor:         req.Cursor,
		Limit:          req.Limit,
		IncludeExpired: req.IncludeExpired,
	})
	if err != nil {
		return drive.SharePage{}, shareCallError(callCtx, err)
	}
	return page, nil
}

// GetShareLink returns one link by its native id, link id, or URL.
func (c *Core) GetShareLink(ctx context.Context, mount, id string) (drive.Share, error) {
	surface, err := c.resolveShareSurface(mount)
	if err != nil {
		return drive.Share{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, c.shareLinkTimeout())
	defer cancel()
	share, err := c.resolveManagedShare(callCtx, surface, id)
	if err != nil {
		return drive.Share{}, shareCallError(callCtx, err)
	}
	return share, nil
}

// UpdateShareLink changes one link's policy. The provider's management id is
// resolved first, so callers may pass a link id.
func (c *Core) UpdateShareLink(ctx context.Context, req ShareLinkUpdateRequest) (drive.Share, error) {
	surface, err := c.resolveShareSurface(req.Mount)
	if err != nil {
		return drive.Share{}, err
	}
	if err := surface.policy.ValidateUpdateRequest(drive.ShareUpdateRequest{}); err != nil {
		return drive.Share{}, shareOptionError(err)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.shareLinkTimeout())
	defer cancel()
	share, err := c.resolveManagedShare(callCtx, surface, req.ID)
	if err != nil {
		return drive.Share{}, shareCallError(callCtx, err)
	}
	update := drive.ShareUpdateRequest{
		ID:       share.NativeID,
		Title:    req.Title,
		Expiry:   req.Expiry,
		Passcode: req.Passcode,
		Public:   req.Public,
	}
	if err := surface.policy.ValidateUpdateRequest(update); err != nil {
		return drive.Share{}, shareOptionError(err)
	}
	updated, err := surface.manager.UpdateShare(callCtx, update)
	if err != nil {
		return drive.Share{}, shareCallError(callCtx, err)
	}
	if updated.NativeID == "" {
		// Providers that answer with an empty body leave the re-read to us.
		updated, err = c.resolveManagedShare(callCtx, surface, share.NativeID)
		if err != nil {
			return drive.Share{}, shareCallError(callCtx, err)
		}
	}
	return updated, nil
}

// RemoveShareLink deletes one link. Callers may pass a link id; the provider's
// management id is resolved first.
func (c *Core) RemoveShareLink(ctx context.Context, mount, id string) error {
	surface, err := c.resolveShareSurface(mount)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, c.shareLinkTimeout())
	defer cancel()
	share, err := c.resolveManagedShare(callCtx, surface, id)
	if err != nil {
		return shareCallError(callCtx, err)
	}
	if err := surface.manager.DeleteShare(callCtx, share.NativeID); err != nil {
		return shareCallError(callCtx, err)
	}
	return nil
}

// resolveShareCreateEntries resolves mount-qualified paths to provider entries.
// Every path MUST exist, belong to the same mount, and name an entry rather than
// the mount root.
func (c *Core) resolveShareCreateEntries(ctx context.Context, paths []string, explicitMount string) ([]drive.Entry, string, error) {
	if len(paths) == 0 {
		return nil, "", fmt.Errorf("%w: at least one path is required", ErrSharePathsRequired)
	}
	wanted := vfstypes.CleanMountName(explicitMount)
	entries := make([]drive.Entry, 0, len(paths))
	mount := ""
	for _, raw := range paths {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, "", fmt.Errorf("%w: empty share path", task.ErrInvalidOperation)
		}
		mountName, dir, err := c.offlineSplitMountPath(raw)
		if err != nil {
			return nil, "", err
		}
		if wanted != "" && mountName != wanted {
			return nil, "", fmt.Errorf("%w: %q is on mount %q, but --mount names %q", ErrShareCrossMount, raw, mountName, wanted)
		}
		if mount == "" {
			mount = mountName
		} else if mount != mountName {
			return nil, "", fmt.Errorf("%w: %q is on mount %q, earlier paths are on %q", ErrShareCrossMount, raw, mountName, mount)
		}
		dir = vfstypes.CleanVirtualPath(dir)
		if dir == "/" {
			return nil, "", fmt.Errorf("%w: %s", ErrShareRootPath, raw)
		}
		entry, err := c.fs.Stat(ctx, c.offlineVirtualPath(mountName, dir))
		if err != nil {
			if vfstypes.IsNotFound(err) {
				return nil, "", fmt.Errorf("%w: %s", ErrSharePathNotFound, raw)
			}
			return nil, "", fmt.Errorf("core: share path %s: %w", raw, err)
		}
		entries = append(entries, entry)
	}
	return entries, mount, nil
}

// resolveShareSurface resolves the acting mount and its share contract. An
// empty mount is accepted only when exactly one mount is available.
func (c *Core) resolveShareSurface(mount string) (shareSurface, error) {
	name := vfstypes.CleanMountName(mount)
	mounts := c.offlineMounts()
	if name == "" {
		var available []vfs.MountInfo
		for _, info := range mounts {
			if info.State != "failed" {
				available = append(available, info)
			}
		}
		switch len(available) {
		case 0:
			return shareSurface{}, fmt.Errorf("%w: no mount is configured", ErrShareMountNotFound)
		case 1:
			name = available[0].Name
		default:
			return shareSurface{}, fmt.Errorf("%w: candidates: %s; pass --mount <name>", ErrShareMountRequired, shareMountNames(available))
		}
	}

	var info vfs.MountInfo
	found := false
	for _, candidate := range mounts {
		if candidate.Name == name {
			info, found = candidate, true
			break
		}
	}
	if !found {
		return shareSurface{}, fmt.Errorf("%w: mount %q", ErrShareMountNotFound, name)
	}
	if info.State == "failed" {
		return shareSurface{}, fmt.Errorf("%w: mount %q: %s", ErrShareMountUnavailable, name, info.Error)
	}
	driver, ok := c.offlineDriver(name)
	if !ok || !drive.HasCapability(driver, drive.CapabilityShare) {
		return shareSurface{}, fmt.Errorf("%w: mount %q", ErrShareUnsupported, name)
	}
	manager, ok := driver.(drive.ShareManager)
	if !ok {
		return shareSurface{}, fmt.Errorf("%w: mount %q", ErrShareUnsupported, name)
	}
	lister, _ := driver.(drive.ShareLister)
	return shareSurface{mount: info, manager: manager, lister: lister, policy: drive.SharePolicyFor(driver)}, nil
}

// resolveManagedShare resolves a user-facing id (native id, link id, or URL) to
// the full link, including the provider's management id.
func (c *Core) resolveManagedShare(ctx context.Context, surface shareSurface, id string) (drive.Share, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return drive.Share{}, fmt.Errorf("%w: link or share id is required", task.ErrInvalidOperation)
	}
	share, getErr := surface.manager.GetShare(ctx, id)
	if getErr == nil && share.NativeID != "" {
		return share, nil
	}
	if surface.policy.List && surface.lister != nil {
		if found, err := findShareByID(ctx, surface.lister, id); err == nil && found != nil {
			return *found, nil
		}
	}
	if getErr != nil {
		return drive.Share{}, getErr
	}
	return drive.Share{}, drive.NewShareError(drive.ShareErrNotFound, "", "no such link")
}

// findShareByID pages through the account's links looking for one identity.
// The page bound keeps a misbehaving provider from turning a lookup into an
// unbounded scan.
func findShareByID(ctx context.Context, lister drive.ShareLister, id string) (*drive.Share, error) {
	const (
		maxPages = 20
		pageSize = 100
	)
	cursor := ""
	for page := 0; page < maxPages; page++ {
		result, err := lister.ListShares(ctx, drive.ShareListRequest{Cursor: cursor, Limit: pageSize, IncludeExpired: true})
		if err != nil {
			return nil, err
		}
		for i := range result.Shares {
			share := &result.Shares[i]
			if share.NativeID == id || share.LinkID == id || share.URL == id {
				return share, nil
			}
		}
		if result.Next == "" {
			return nil, nil
		}
		cursor = result.Next
	}
	return nil, nil
}

// shareOptionError keeps both identities on the error chain: the core sentinel
// (CLI usage classification) and the provider-neutral share code (scripts and
// task detail).
func shareOptionError(err error) error {
	return fmt.Errorf("%w: %w", ErrShareUnsupported, err)
}

func shareMountNames(mounts []vfs.MountInfo) string {
	names := make([]string, 0, len(mounts))
	for _, info := range mounts {
		names = append(names, info.Name)
	}
	return strings.Join(names, ", ")
}

func (c *Core) shareLinkTimeout() time.Duration {
	if c != nil && c.shareLinkOpTimeout > 0 {
		return c.shareLinkOpTimeout
	}
	return defaultShareLinkTimeout
}

// shareCallError turns a context deadline into the stable share_timeout code so
// every synchronous link operation reports a hang the same way.
func shareCallError(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return drive.NewShareError(drive.ShareErrTimeout, "", "provider did not finish in time")
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return err
}

// neutralPasscodeError keeps a passcode failure provider-neutral. A caller
// matches the stable code; the provider's own wording is not part of the
// contract and must not be the message a script or a user sees.
func neutralPasscodeError(err error) error {
	var share *drive.ShareError
	if !errors.As(err, &share) {
		return err
	}
	switch share.Code {
	case drive.ShareErrPasscodeRequired, drive.ShareErrPasscodeWrong:
		return drive.NewShareError(share.Code, share.ProviderCode, "")
	default:
		return err
	}
}

// GenerateSharePasscode returns a four-character passcode from an alphabet that
// omits visually ambiguous characters.
func GenerateSharePasscode() (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("core: generate share passcode: %w", err)
	}
	out := make([]byte, len(buffer))
	for i, value := range buffer {
		out[i] = alphabet[int(value)%len(alphabet)]
	}
	return string(out), nil
}
