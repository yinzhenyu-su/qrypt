package koofr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// shareSessionData is the opaque share-session payload carried between
// OpenShare and the listing/fetch calls. It never contains a passcode.
type shareSessionData struct {
	LinkID       string `json:"link_id"`
	Path         string `json:"path"`
	HasPassword  bool   `json:"has_password"`
	Downloadable bool   `json:"downloadable"`
}

func (d *Driver) SharePolicy() drive.SharePolicy {
	return drive.SharePolicy{
		Passcode:   drive.PolicyOptional,
		Expiries:   drive.NeutralShareExpiries(),
		Download:   true,
		Update:     true,
		List:       true,
		MaxEntries: 1,
		Selection:  true,
	}
}

func (d *Driver) linksPath(suffix string) string {
	return "/mounts/" + url.PathEscape(d.mountID) + "/links" + suffix
}

// CreateShare publishes a link for exactly one entry. Koofr links have no
// title, visit-limit, public, or edit concept; the policy layer rejects
// those before this runs.
func (d *Driver) CreateShare(ctx context.Context, req drive.ShareCreateRequest) (drive.Share, error) {
	if len(req.Entries) != 1 {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "koofr links cover exactly one entry")
	}
	entry := req.Entries[0]
	if d.virtualRoot() {
		// Koofr link endpoints are mount-scoped; a link created on an
		// external mount could not be read, updated, or deleted through this
		// driver's primary-mount endpoints. Require an explicit mount_id
		// mount to share external-mount entries.
		if _, _, ext := splitExtID(strings.TrimSpace(entry.ID)); ext {
			return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "sharing external-mount entries requires a mount configured with an explicit mount_id")
		}
	}
	path, err := d.resolveWithin(entry.ID, "share")
	if err != nil || path == "/" {
		return drive.Share{}, drive.NewShareError(drive.ShareErrLinkInvalid, "", "entry path is required")
	}
	// Unlike UpdateShare (where a negative expiry clears the validity
	// window), creating a link with a negative expiry is a caller bug.
	if req.Expiry < 0 {
		return drive.Share{}, fmt.Errorf("koofr: negative share expiry %d: %w", req.Expiry, drive.ErrInvalidInput)
	}
	if err := d.SharePolicy().ValidateCreateRequest(req); err != nil {
		return drive.Share{}, err
	}
	passcode := strings.TrimSpace(req.Passcode)
	if passcode != "" && !validPasscode(passcode) {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "koofr link passcodes must be exactly 6 letters or digits")
	}
	resp, err := d.cl.postOnce(ctx, d.linksPath(""), nil, map[string]any{"path": path}, nil)
	if err != nil {
		return drive.Share{}, shareErrorFromHTTP(err)
	}
	var created apiLink
	if err := json.Unmarshal(resp.body, &created); err != nil || created.ID == "" {
		return drive.Share{}, drive.NewShareError(drive.ShareErrProvider, "", "koofr returned no link")
	}
	linkID := created.ID
	cleanup := true
	defer func() {
		if cleanup {
			_ = d.DeleteShare(context.WithoutCancel(ctx), linkID)
		}
	}()

	if passcode != "" {
		if _, err = d.setLinkPassword(ctx, linkID, passcode); err != nil {
			return drive.Share{}, err
		}
	}
	if req.Download != nil {
		if _, err = d.setLinkDownloadable(ctx, linkID, *req.Download); err != nil {
			return drive.Share{}, err
		}
	}
	if req.Expiry > 0 {
		if _, err = d.setLinkValidity(ctx, linkID, time.Now().Add(req.Expiry)); err != nil {
			return drive.Share{}, err
		}
	}
	final, err := d.GetShare(ctx, linkID)
	if err != nil {
		return drive.Share{}, err
	}
	final.Passcode = passcode
	cleanup = false
	return final, nil
}

// GetShare fetches one link on the mounted provider mount.
func (d *Driver) GetShare(ctx context.Context, id string) (drive.Share, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return drive.Share{}, drive.NewShareError(drive.ShareErrNotFound, "", "link id is required")
	}
	var link apiLink
	if _, err := d.cl.get(ctx, d.linksPath("/"+url.PathEscape(id)), nil, &link); err != nil {
		return drive.Share{}, shareErrorFromHTTP(err)
	}
	if link.ID == "" {
		return drive.Share{}, drive.NewShareError(drive.ShareErrNotFound, "", "link not found")
	}
	return shareFromLink(link), nil
}

// UpdateShare changes a link's passcode, expiry, or downloadable flag.
// Removing a passcode is rejected: the provider's password/reset replaces it
// with a random password instead of clearing it.
func (d *Driver) UpdateShare(ctx context.Context, req drive.ShareUpdateRequest) (drive.Share, error) {
	if req.Title != nil {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "koofr links do not support titles")
	}
	if req.Public != nil {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "koofr links have no public/private toggle")
	}
	if req.Edit != nil {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "koofr links do not support edit permission")
	}
	if req.Passcode != nil && strings.TrimSpace(*req.Passcode) == "" {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "koofr links cannot remove a passcode (reset replaces it with a random one)")
	}
	if req.Passcode != nil && !validPasscode(strings.TrimSpace(*req.Passcode)) {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "koofr link passcodes must be exactly 6 letters or digits")
	}
	if req.Passcode == nil && req.Expiry == nil && req.Download == nil {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "nothing to update")
	}
	id := strings.TrimSpace(req.ID)
	if _, err := d.GetShare(ctx, id); err != nil {
		return drive.Share{}, err
	}
	if req.Passcode != nil {
		if _, err := d.setLinkPassword(ctx, id, strings.TrimSpace(*req.Passcode)); err != nil {
			return drive.Share{}, err
		}
	}
	if req.Expiry != nil {
		expiry := time.Time{}
		if *req.Expiry > 0 {
			expiry = time.Now().Add(*req.Expiry)
		}
		if _, err := d.setLinkValidity(ctx, id, expiry); err != nil {
			return drive.Share{}, err
		}
	}
	if req.Download != nil {
		if _, err := d.setLinkDownloadable(ctx, id, *req.Download); err != nil {
			return drive.Share{}, err
		}
	}
	updated, err := d.GetShare(ctx, id)
	if err != nil {
		return drive.Share{}, err
	}
	if req.Passcode != nil {
		updated.Passcode = strings.TrimSpace(*req.Passcode)
	}
	return updated, nil
}

// DeleteShare removes a link; the provider answers 204.
func (d *Driver) DeleteShare(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return drive.NewShareError(drive.ShareErrNotFound, "", "link id is required")
	}
	resp, err := d.cl.deleteOnce(ctx, d.linksPath("/"+url.PathEscape(id)), nil, nil)
	if err != nil {
		if resp != nil && resp.status() == httpStatusNotFound {
			return drive.NewShareError(drive.ShareErrNotFound, "", "link not found")
		}
		return shareErrorFromHTTP(err)
	}
	return nil
}

// ListShares lists every link on the mounted provider mount.
func (d *Driver) ListShares(ctx context.Context, req drive.ShareListRequest) (drive.SharePage, error) {
	// Live probing showed both the mount-scoped and the account-level link
	// listings return an enveloped {"links":[...]} object, not a bare array
	// (the account-level /api/v2.1/links adds a "mounts" map).
	var enveloped struct {
		Links []apiLink `json:"links"`
	}
	if _, err := d.cl.get(ctx, d.linksPath(""), nil, &enveloped); err != nil {
		return drive.SharePage{}, shareErrorFromHTTP(err)
	}
	page := drive.SharePage{}
	now := time.Now().UnixMilli()
	for _, link := range enveloped.Links {
		if link.ID == "" {
			continue
		}
		if !req.IncludeExpired && link.ValidTo > 0 && link.ValidTo < now {
			continue
		}
		page.Shares = append(page.Shares, shareFromLink(link))
	}
	return page, nil
}

// validPasscode reports whether the passcode satisfies the provider's link
// password charset: exactly 6 ASCII letters or digits. Invalid passcodes are
// rejected client-side because the provider answers them with an opaque 400.
func validPasscode(passcode string) bool {
	if len(passcode) != 6 {
		return false
	}
	for i := 0; i < len(passcode); i++ {
		c := passcode[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// setLinkPassword calls password/set. The response echoes the plaintext
// password; it is parsed for state and never logged or stored. The passcode
// is also scrubbed from any error text in case a provider failure message
// quotes the request.
func (d *Driver) setLinkPassword(ctx context.Context, id, password string) (apiLink, error) {
	resp, err := d.cl.put(ctx, d.linksPath("/"+url.PathEscape(id)+"/password/set"), nil, map[string]any{"password": password}, nil)
	if err != nil {
		return apiLink{}, scrubSecret(shareErrorFromHTTP(err), password)
	}
	var link apiLink
	if err := json.Unmarshal(resp.body, &link); err != nil || link.ID == "" {
		return apiLink{}, drive.NewShareError(drive.ShareErrProvider, "", "koofr returned no link after password/set")
	}
	link.Password = ""
	return link, nil
}

func (d *Driver) setLinkValidity(ctx context.Context, id string, expiry time.Time) (apiLink, error) {
	payload := map[string]any{"validFrom": 0, "validTo": 0}
	if !expiry.IsZero() {
		payload["validTo"] = expiry.UnixMilli()
	}
	resp, err := d.cl.put(ctx, d.linksPath("/"+url.PathEscape(id)+"/validity"), nil, payload, nil)
	if err != nil {
		return apiLink{}, shareErrorFromHTTP(err)
	}
	var link apiLink
	if err := json.Unmarshal(resp.body, &link); err != nil || link.ID == "" {
		return apiLink{}, drive.NewShareError(drive.ShareErrProvider, "", "koofr returned no link after validity update")
	}
	return link, nil
}

func (d *Driver) setLinkDownloadable(ctx context.Context, id string, downloadable bool) (apiLink, error) {
	resp, err := d.cl.put(ctx, d.linksPath("/"+url.PathEscape(id)+"/downloadable"), nil, map[string]any{"downloadable": downloadable}, nil)
	if err != nil {
		return apiLink{}, shareErrorFromHTTP(err)
	}
	var link apiLink
	if err := json.Unmarshal(resp.body, &link); err != nil || link.ID == "" {
		return apiLink{}, drive.NewShareError(drive.ShareErrProvider, "", "koofr returned no link after downloadable update")
	}
	return link, nil
}

// shareLinkDefaultHost is the provider's canonical page host.
const shareLinkDefaultHost = "app.koofr.net"

// shareLinkHost returns the page host derived from the configured
// api_base_url, so a driver pointed at a custom gateway accepts links on
// that gateway too.
func (d *Driver) shareLinkHost() string {
	if u, err := url.Parse(d.cl.base); err == nil && u.Hostname() != "" {
		return strings.ToLower(u.Hostname())
	}
	return shareLinkDefaultHost
}

// ParseShareLink recognizes https://app.koofr.net/links/{id} page URLs (or
// the equivalent page on a driver configured with a custom api_base_url
// host, e.g. an on-premise gateway). The https://k00.fr/{hash} short form is
// deliberately not parsed: its path segment is a redirect hash, not the link
// id, and resolving it would need a network round trip this synchronous
// parser cannot make. Producing a wrong identity would send every later link
// operation to a 404, so short URLs are rejected instead.
func (d *Driver) ParseShareLink(raw string) (drive.ShareLink, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return drive.ShareLink{}, false
	}
	host := strings.ToLower(u.Hostname())
	if host != d.shareLinkHost() && host != strings.ToLower(shareLinkDefaultHost) {
		return drive.ShareLink{}, false
	}
	rest := strings.Trim(u.Path, "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] != "links" {
		return drive.ShareLink{}, false
	}
	id := parts[1]
	if id == "" || strings.Contains(id, "/") {
		return drive.ShareLink{}, false
	}
	link := drive.ShareLink{URL: raw, LinkID: id}
	link.Passcode = u.Query().Get("password")
	return link, true
}

// OpenShare opens a link for browsing. The provider exposes no anonymous
// listing API, so receive works for links the acting account can see on this
// mount (its own links); a link that cannot be resolved reports not-found.
func (d *Driver) OpenShare(ctx context.Context, link drive.ShareLink) (drive.ShareSession, error) {
	if strings.TrimSpace(link.LinkID) == "" {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrLinkInvalid, "", "link id is required")
	}
	var api apiLink
	resp, err := d.cl.get(ctx, d.linksPath("/"+url.PathEscape(link.LinkID)), nil, &api)
	if err != nil {
		if resp != nil && resp.status() == httpStatusNotFound {
			return drive.ShareSession{}, drive.NewShareError(drive.ShareErrNotFound, "", "link not found on this mount (browsing foreign Koofr links is not supported)")
		}
		return drive.ShareSession{}, shareErrorFromHTTP(err)
	}
	if api.ID == "" {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrNotFound, "", "link not found")
	}
	if api.Path == "" {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrLinkInvalid, "", "link has no path")
	}
	if api.ValidTo > 0 && api.ValidTo < time.Now().UnixMilli() {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrExpired, "", "link has expired")
	}
	if api.HasPassword && strings.TrimSpace(link.Passcode) == "" {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrPasscodeRequired, "", "link requires a passcode")
	}
	data := shareSessionData{
		LinkID:       api.ID,
		Path:         api.Path,
		HasPassword:  api.HasPassword,
		Downloadable: api.Downloadable,
	}
	token, err := json.Marshal(data)
	if err != nil {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrProvider, "", "encode share session")
	}
	share := shareFromLink(api)
	expires := time.Time{}
	if api.ValidTo > 0 {
		expires = time.UnixMilli(api.ValidTo).UTC()
	}
	return drive.ShareSession{
		Link:         link,
		SessionToken: string(token),
		ExpiresAt:    expires,
		Share:        share,
	}, nil
}

// ListShareEntries lists the linked directory's contents through the
// authenticated listing API, one page at a time (Koofr has no share
// pagination, so every page after the first is empty).
func (d *Driver) ListShareEntries(ctx context.Context, session drive.ShareSession, dir drive.SharePath, cursor string) (drive.ShareEntryPage, error) {
	var data shareSessionData
	if err := json.Unmarshal([]byte(session.SessionToken), &data); err != nil || data.LinkID == "" {
		return drive.ShareEntryPage{}, drive.NewShareError(drive.ShareErrLinkInvalid, "", "invalid share session")
	}
	if cursor != "" {
		return drive.ShareEntryPage{}, nil
	}
	if data.Path == "" {
		return drive.ShareEntryPage{}, drive.NewShareError(drive.ShareErrLinkInvalid, "", "link has no path")
	}
	linkRoot, err := cleanMountPath(data.Path)
	if err != nil {
		return drive.ShareEntryPage{}, drive.NewShareError(drive.ShareErrLinkInvalid, "", "invalid link path")
	}
	root := linkRoot
	switch strings.TrimSpace(dir.ID) {
	case "", "0", "/":
	default:
		root, err = cleanMountPath(dir.ID)
		if err != nil {
			return drive.ShareEntryPage{}, drive.NewShareError(drive.ShareErrLinkInvalid, "", "invalid share directory")
		}
	}
	if !isWithin(root, linkRoot) {
		return drive.ShareEntryPage{}, drive.NewShareError(drive.ShareErrLinkInvalid, "", "directory escapes the link")
	}
	// A link can point at a single file. The provider has no anonymous
	// listing for that case, so synthesize the one-entry root page instead of
	// asking files/list for a child directory that does not exist.
	if root == linkRoot && linkRoot != "/" {
		info, err := d.infoOn(ctx, opTarget{mount: apiMount{ID: d.mountID}}, linkRoot)
		if err != nil {
			return drive.ShareEntryPage{}, shareErrorFromHTTP(err)
		}
		if !info.IsDir {
			return drive.ShareEntryPage{
				Entries: []drive.ShareEntry{{Entry: info, RootID: linkRoot}},
			}, nil
		}
	}
	entries, err := d.listDirOn(ctx, d.mountID, root, root)
	if err != nil {
		return drive.ShareEntryPage{}, shareErrorFromHTTP(err)
	}
	page := drive.ShareEntryPage{}
	for _, entry := range entries {
		page.Entries = append(page.Entries, drive.ShareEntry{Entry: entry, RootID: root})
	}
	return page, nil
}

// ShareDownloadTargets hands out anonymous content-link download targets,
// one per selected file. Directory paths return a zip upstream and are
// rejected; passcode-protected links need the passcode; non-downloadable
// links refuse fetches.
func (d *Driver) ShareDownloadTargets(ctx context.Context, req drive.ShareFetchRequest) ([]drive.DownloadTarget, error) {
	var data shareSessionData
	if err := json.Unmarshal([]byte(req.Session.SessionToken), &data); err != nil || data.LinkID == "" {
		return nil, drive.NewShareError(drive.ShareErrLinkInvalid, "", "invalid share session")
	}
	if !data.Downloadable {
		return nil, drive.NewShareError(drive.ShareErrUnsupported, "", "link does not allow download")
	}
	if data.HasPassword && req.Session.Link.Passcode == "" {
		return nil, drive.NewShareError(drive.ShareErrPasscodeRequired, "", "link requires a passcode")
	}
	linkRoot, err := cleanMountPath(data.Path)
	if err != nil || data.Path == "" {
		return nil, drive.NewShareError(drive.ShareErrLinkInvalid, "", "invalid link path")
	}
	targets := make([]drive.DownloadTarget, 0, len(req.Entries))
	for _, entry := range req.Entries {
		if entry.IsDir {
			return nil, drive.NewShareError(drive.ShareErrUnsupported, "", "koofr share fetch supports files only (directories return a zip)")
		}
		entryPath, err := cleanMountPath(entry.ID)
		if err != nil {
			return nil, drive.NewShareError(drive.ShareErrLinkInvalid, "", "invalid share entry")
		}
		if linkRoot != "/" && !isWithin(entryPath, linkRoot) {
			return nil, drive.NewShareError(drive.ShareErrLinkInvalid, "", "entry escapes the link")
		}
		rel := entryPath
		if linkRoot != "/" {
			rel = strings.TrimPrefix(rel, linkRoot)
		}
		// rel is either provider-absolute (link rooted at the mount root) or
		// already relative; normalize to a single leading slash so the
		// anonymous URL never carries a "//" path segment.
		rel = strings.TrimPrefix(rel, "/")
		query := url.Values{}
		query.Set("path", "/"+rel)
		if req.Session.Link.Passcode != "" {
			query.Set("password", req.Session.Link.Passcode)
		}
		target := d.cl.base + "/content/links/" + url.PathEscape(data.LinkID) + "/files/get?" + query.Encode()
		// The anonymous content route answers non-browser user agents with
		// 400 "Hotlinking not allowed" unless the request carries the link
		// page as its Referer (live-verified with a Go user agent).
		targets = append(targets, drive.DownloadTarget{
			URL:     target,
			Size:    entry.Size,
			Headers: map[string]string{"Referer": d.cl.base + "/links/" + url.PathEscape(data.LinkID)},
		})
	}
	return targets, nil
}

func shareFromLink(link apiLink) drive.Share {
	share := drive.Share{
		NativeID:  link.ID,
		LinkID:    link.ID,
		URL:       link.URL,
		Title:     link.Name,
		Public:    !link.HasPassword,
		CreatedAt: msTime(link.Created),
	}
	if link.ShortURL != "" && share.URL == "" {
		share.URL = link.ShortURL
	}
	if link.ValidTo > 0 {
		share.ExpiresAt = msTime(link.ValidTo)
	}
	return share
}

func msTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func shareErrorFromHTTP(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, drive.ErrNotFound):
		return drive.NewShareError(drive.ShareErrNotFound, "", err.Error())
	case errors.Is(err, drive.ErrRateLimit):
		return drive.NewShareError(drive.ShareErrRateLimited, "", err.Error())
	case errors.Is(err, drive.ErrAuth):
		return drive.NewShareError(drive.ShareErrProvider, "", err.Error())
	case errors.Is(err, errMountNotSynced):
		return drive.NewShareError(drive.ShareErrProvider, "FilesNotSynced", err.Error())
	default:
		return drive.NewShareError(drive.ShareErrProvider, "", err.Error())
	}
}

const httpStatusNotFound = 404

// secretScrubbedError hides a secret from an error's text while preserving
// its chain, so classification (errors.Is/As) keeps working.
type secretScrubbedError struct {
	err    error
	secret string
}

func (e secretScrubbedError) Error() string {
	return strings.ReplaceAll(e.err.Error(), e.secret, "<masked>")
}

func (e secretScrubbedError) Unwrap() error { return e.err }

func scrubSecret(err error, secret string) error {
	if err == nil || secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}
	return secretScrubbedError{err: err, secret: secret}
}
