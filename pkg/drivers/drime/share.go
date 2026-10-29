package drime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) SharePolicy() drive.SharePolicy {
	return drive.SharePolicy{
		Passcode:  drive.PolicyOptional,
		Expiries:  drive.NeutralShareExpiries(),
		Update:    true,
		List:      false,
		Selection: true,
	}
}

func (d *Driver) CreateShare(ctx context.Context, req drive.ShareCreateRequest) (drive.Share, error) {
	if len(req.Entries) != 1 {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "drime links cover exactly one entry")
	}
	if strings.TrimSpace(req.Title) != "" {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "drime links do not support titles")
	}
	if req.VisitLimit > 0 {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "drime links do not support visit limits")
	}
	entry := req.Entries[0]
	payload := map[string]any{
		"allow_download": true,
		"allow_edit":     false,
	}
	if req.Passcode != "" {
		payload["password"] = req.Passcode
	}
	if req.Expiry > 0 {
		payload["expires_at"] = time.Now().Add(req.Expiry).UTC().Format("2006-01-02T15:04:05.000000Z")
	}
	resp, err := d.cl.post(ctx, "/file-entries/"+url.PathEscape(entry.ID)+"/shareable-link", payload, nil)
	if err != nil {
		d.setLastError(err)
		return drive.Share{}, err
	}
	var body apiShareLinkResponse
	if err := json.Unmarshal(resp.body, &body); err != nil {
		return drive.Share{}, fmt.Errorf("drime: decode share create: %w", err)
	}
	if body.Link == nil {
		return drive.Share{}, drive.NewShareError(drive.ShareErrProvider, "", "drime returned no link")
	}
	share := shareFromLink(body.Link, entry, req.Passcode)
	if share.NativeID == "" {
		share.NativeID = entry.ID
	}
	return share, nil
}

func (d *Driver) GetShare(ctx context.Context, id string) (drive.Share, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return drive.Share{}, drive.NewShareError(drive.ShareErrNotFound, "", "link id is required")
	}
	var body apiShareLinkResponse
	if _, err := strconv.ParseInt(id, 10, 64); err == nil {
		path := "/file-entries/" + url.PathEscape(id) + "/shareable-link?workspaceId=" + url.QueryEscape(d.workspaceID)
		if _, err := d.cl.get(ctx, path, &body); err != nil {
			return drive.Share{}, shareErrorFromHTTP(err)
		}
	} else {
		if _, err := d.cl.get(ctx, "/shareable-links/"+url.PathEscape(id), &body); err != nil {
			return drive.Share{}, shareErrorFromHTTP(err)
		}
	}
	if body.Link == nil {
		return drive.Share{}, drive.NewShareError(drive.ShareErrNotFound, "", "link not found")
	}
	entry := drive.Entry{}
	if body.Link.Entry != nil {
		entry = body.Link.Entry.entry("")
	}
	return shareFromLink(body.Link, entry, ""), nil
}

func (d *Driver) ListShares(context.Context, drive.ShareListRequest) (drive.SharePage, error) {
	return drive.SharePage{}, drive.NewShareError(drive.ShareErrUnsupported, "", "drime does not expose an account-wide link list")
}

func (d *Driver) UpdateShare(ctx context.Context, req drive.ShareUpdateRequest) (drive.Share, error) {
	if req.Title != nil {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "drime links do not support titles")
	}
	if req.Public != nil {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "drime links have no public/private toggle")
	}
	current, err := d.GetShare(ctx, req.ID)
	if err != nil {
		return drive.Share{}, err
	}
	if current.NativeID == "" {
		return drive.Share{}, drive.NewShareError(drive.ShareErrNotFound, "", "link entry id is unavailable")
	}
	payload := map[string]any{}
	if req.Passcode != nil {
		payload["password"] = *req.Passcode
	}
	if req.Expiry != nil {
		if *req.Expiry > 0 {
			payload["expiresAt"] = time.Now().Add(*req.Expiry).UTC().Format("2006-01-02T15:04:05.000000Z")
		} else {
			payload["expiresAt"] = nil
		}
	}
	resp, err := d.cl.doJSON(ctx, http.MethodPut, "/file-entries/"+url.PathEscape(current.NativeID)+"/shareable-link?workspaceId="+url.QueryEscape(d.workspaceID), payload, true)
	if err != nil {
		return drive.Share{}, shareErrorFromHTTP(err)
	}
	if !resp.ok() {
		return drive.Share{}, shareErrorFromHTTP(resp.error("drime: update share"))
	}
	var body apiShareLinkResponse
	if err := json.Unmarshal(resp.body, &body); err != nil {
		return drive.Share{}, fmt.Errorf("drime: decode share update: %w", err)
	}
	if body.Link == nil {
		return drive.Share{}, drive.NewShareError(drive.ShareErrProvider, "", "drime returned no link")
	}
	return shareFromLink(body.Link, drive.Entry{}, ""), nil
}

func (d *Driver) DeleteShare(ctx context.Context, id string) error {
	current, err := d.GetShare(ctx, id)
	if err != nil {
		return err
	}
	if current.NativeID == "" {
		return drive.NewShareError(drive.ShareErrNotFound, "", "link entry id is unavailable")
	}
	resp, err := d.cl.doJSON(ctx, http.MethodDelete, "/file-entries/"+url.PathEscape(current.NativeID)+"/shareable-link", nil, true)
	if err != nil {
		return shareErrorFromHTTP(err)
	}
	if resp.status() == http.StatusNotFound {
		return drive.NewShareError(drive.ShareErrNotFound, "", "link not found")
	}
	if !resp.ok() {
		return shareErrorFromHTTP(resp.error("drime: delete share"))
	}
	return nil
}

func (d *Driver) ParseShareLink(raw string) (drive.ShareLink, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return drive.ShareLink{}, false
	}
	host := strings.ToLower(u.Hostname())
	var hash string
	switch host {
	case "dri.me":
		hash = strings.Trim(strings.TrimPrefix(u.Path, "/"), "/")
	case "app.drime.cloud":
		rest := strings.TrimPrefix(u.Path, "/")
		if !strings.HasPrefix(rest, "drive/s/") {
			return drive.ShareLink{}, false
		}
		hash = strings.TrimPrefix(rest, "drive/s/")
	default:
		return drive.ShareLink{}, false
	}
	if i := strings.IndexByte(hash, ':'); i >= 0 {
		hash = hash[:i]
	}
	if hash == "" || strings.Contains(hash, "/") {
		return drive.ShareLink{}, false
	}
	passcode := u.Query().Get("pwd")
	if passcode == "" {
		passcode = u.Query().Get("password")
	}
	return drive.ShareLink{URL: raw, LinkID: hash, Passcode: passcode}, true
}

type shareSessionData struct {
	Hash          string `json:"hash"`
	LinkID        int64  `json:"link_id"`
	Password      string `json:"password,omitempty"`
	AllowDownload bool   `json:"allow_download"`
}

func (d *Driver) OpenShare(ctx context.Context, link drive.ShareLink) (drive.ShareSession, error) {
	if link.LinkID == "" {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrLinkInvalid, "", "link hash is required")
	}
	params := url.Values{}
	if link.Passcode != "" {
		params.Set("password", link.Passcode)
	}
	query := ""
	if encoded := params.Encode(); encoded != "" {
		query = "?" + encoded
	}
	resp, err := d.cl.doJSON(ctx, http.MethodGet, "/shareable-links/"+url.PathEscape(link.LinkID)+query, nil, false)
	if err != nil {
		return drive.ShareSession{}, shareErrorFromHTTP(err)
	}
	if resp.status() == http.StatusForbidden {
		var errBody apiShareLinkResponse
		_ = json.Unmarshal(resp.body, &errBody)
		if errBody.PasswordInvalid {
			if link.Passcode == "" {
				return drive.ShareSession{}, drive.NewShareError(drive.ShareErrPasscodeRequired, "", "link requires a passcode")
			}
			return drive.ShareSession{}, drive.NewShareError(drive.ShareErrPasscodeWrong, "", "link passcode is wrong")
		}
	}
	if resp.status() == http.StatusNotFound {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrNotFound, "", "link not found")
	}
	if !resp.ok() {
		return drive.ShareSession{}, shareErrorFromHTTP(resp.error("drime: open share"))
	}
	var body apiShareLinkResponse
	if err := json.Unmarshal(resp.body, &body); err != nil {
		return drive.ShareSession{}, fmt.Errorf("drime: decode share link: %w", err)
	}
	if body.Link == nil {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrNotFound, "", "link not found")
	}
	if body.Link.RevokedAt != nil && *body.Link.RevokedAt != "" {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrExpired, "", "link was revoked")
	}
	if body.Link.ExpiresAt != nil && *body.Link.ExpiresAt != "" {
		if expires := parseAPITime(*body.Link.ExpiresAt); !expires.IsZero() && time.Now().After(expires) {
			return drive.ShareSession{}, drive.NewShareError(drive.ShareErrExpired, "", "link expired")
		}
	}
	entry := drive.Entry{}
	if body.Link.Entry != nil {
		entry = body.Link.Entry.entry("")
	}
	share := shareFromLink(body.Link, entry, link.Passcode)
	data := shareSessionData{
		Hash:          body.Link.Hash,
		LinkID:        body.Link.ID,
		Password:      link.Passcode,
		AllowDownload: body.Link.AllowDownload,
	}
	token := string(mustJSONRaw(data))
	return drive.ShareSession{
		Link:         link,
		SessionToken: token,
		ExpiresAt:    share.ExpiresAt,
		Share:        share,
	}, nil
}

func (d *Driver) ListShareEntries(ctx context.Context, session drive.ShareSession, dir drive.SharePath, cursor string) (drive.ShareEntryPage, error) {
	var data shareSessionData
	if err := json.Unmarshal([]byte(session.SessionToken), &data); err != nil || data.Hash == "" {
		return drive.ShareEntryPage{}, drive.NewShareError(drive.ShareErrLinkInvalid, "", "invalid share session")
	}
	page := 1
	if cursor != "" {
		if parsed, err := strconv.Atoi(cursor); err == nil && parsed > 0 {
			page = parsed
		}
	}
	base := data.Hash
	if dir.ID != "" {
		base = data.Hash + ":" + dir.ID
	}
	q := url.Values{}
	q.Set("withEntries", "true")
	q.Set("page", strconv.Itoa(page))
	q.Set("order", "updated_at:desc")
	if data.Password != "" {
		q.Set("password", data.Password)
	}
	resp, err := d.cl.doJSON(ctx, http.MethodGet, "/shareable-links/"+url.PathEscape(base)+"?"+q.Encode(), nil, false)
	if err != nil {
		return drive.ShareEntryPage{}, shareErrorFromHTTP(err)
	}
	if resp.status() == http.StatusForbidden {
		if data.Password == "" {
			return drive.ShareEntryPage{}, drive.NewShareError(drive.ShareErrPasscodeRequired, "", "link requires a passcode")
		}
		return drive.ShareEntryPage{}, drive.NewShareError(drive.ShareErrPasscodeWrong, "", "link passcode is wrong")
	}
	if !resp.ok() {
		return drive.ShareEntryPage{}, shareErrorFromHTTP(resp.error("drime: list share"))
	}
	var body apiShareLinkResponse
	if err := json.Unmarshal(resp.body, &body); err != nil {
		return drive.ShareEntryPage{}, fmt.Errorf("drime: decode share listing: %w", err)
	}
	if body.FolderChildren == nil {
		return drive.ShareEntryPage{}, nil
	}
	pageData := body.FolderChildren
	out := make([]drive.ShareEntry, 0, len(pageData.Data))
	for _, item := range pageData.Data {
		mapped := item.entry(dir.ID)
		if item.Hash != "" {
			mapped.ID = item.Hash
		}
		out = append(out, drive.ShareEntry{Entry: mapped, RootID: dir.ID})
	}
	next := ""
	if pageData.CurrentPage > 0 && pageData.CurrentPage < pageData.LastPage {
		next = strconv.Itoa(pageData.CurrentPage + 1)
	}
	return drive.ShareEntryPage{Entries: out, Next: next}, nil
}

func (d *Driver) ShareDownloadTargets(ctx context.Context, req drive.ShareFetchRequest) ([]drive.DownloadTarget, error) {
	var data shareSessionData
	if err := json.Unmarshal([]byte(req.Session.SessionToken), &data); err != nil || data.Hash == "" {
		return nil, drive.NewShareError(drive.ShareErrLinkInvalid, "", "invalid share session")
	}
	if !data.AllowDownload {
		return nil, drive.NewShareError(drive.ShareErrUnsupported, "", "link does not allow download")
	}
	targets := make([]drive.DownloadTarget, 0, len(req.Entries))
	for _, entry := range req.Entries {
		if entry.IsDir {
			return nil, drive.NewShareError(drive.ShareErrUnsupported, "", "drime share fetch supports files only")
		}
		if raw, ok := drive.RawEntryExtra(entry.Extra).(map[string]any); ok {
			if perms, ok := raw["permissions"].(map[string]bool); ok {
				if allowed, exists := perms["files.download"]; exists && !allowed {
					return nil, drive.NewShareError(drive.ShareErrUnsupported, "", "share does not allow download")
				}
			}
		}
		q := url.Values{}
		q.Set("shareable_link", strconv.FormatInt(data.LinkID, 10))
		if data.Password != "" {
			q.Set("password", data.Password)
		}
		target := d.cl.base + "/file-entries/download/" + url.PathEscape(entry.ID) + "?" + q.Encode()
		targets = append(targets, drive.DownloadTarget{URL: target, Size: entry.Size})
	}
	return targets, nil
}

func (d *Driver) SaveShareEntries(ctx context.Context, req drive.ShareSaveRequest) (drive.ShareSaveOutcome, error) {
	var data shareSessionData
	if err := json.Unmarshal([]byte(req.Session.SessionToken), &data); err != nil || data.Hash == "" {
		return drive.ShareSaveOutcome{}, drive.NewShareError(drive.ShareErrLinkInvalid, "", "invalid share session")
	}
	if len(req.Entries) == 0 {
		return drive.ShareSaveOutcome{}, drive.NewShareError(drive.ShareErrUnsupported, "", "no entries selected")
	}
	destID := strings.TrimSpace(req.Dest.Entry.ID)
	workspace := workspaceOf(req.Dest.Entry, d.workspaceID)
	var destination any
	if destID != "" {
		destination = json.Number(destID)
	}
	out := drive.ShareSaveOutcome{State: drive.SaveStateSucceeded}
	succeeded := 0
	for _, source := range req.Entries {
		item := drive.ShareSaveItem{SourceID: source.ID, SourceName: source.Name, SourceSize: source.Size}
		payload := map[string]any{
			"hash":                   data.Hash,
			"destinationId":          destination,
			"destinationWorkspaceId": json.Number(workspace),
		}
		if data.Password != "" {
			payload["password"] = data.Password
		}
		if source.ID != "" {
			payload["entryId"] = source.ID
		}
		resp, err := d.cl.post(ctx, "/shareable-links/"+url.PathEscape(data.Hash)+"/import", payload, nil)
		if err != nil {
			item.Error = drive.NewShareError(drive.ShareErrProvider, "", err.Error())
			out.State = drive.SaveStatePartial
			out.Items = append(out.Items, item)
			continue
		}
		var imported apiImportResponse
		_ = json.Unmarshal(resp.body, &imported)
		var saved *drive.Entry
		switch {
		case imported.Entry != nil && imported.Entry.ID != 0:
			mapped := imported.Entry.entry(destID)
			saved = &mapped
		case imported.FileEntry != nil && imported.FileEntry.ID != 0:
			mapped := imported.FileEntry.entry(destID)
			saved = &mapped
		case len(imported.Entries) > 0:
			mapped := imported.Entries[0].entry(destID)
			saved = &mapped
		default:
			if found, findErr := d.findSavedEntry(ctx, destID, source.Name); findErr == nil && found != nil {
				saved = found
			}
		}
		if saved == nil {
			item.Error = drive.NewShareError(drive.ShareErrDestinationUnverified, "", "import succeeded but the saved entry was not found")
			out.State = drive.SaveStatePartial
		} else {
			item.Saved = saved
			succeeded++
		}
		out.Items = append(out.Items, item)
	}
	if succeeded == 0 {
		out.State = drive.SaveStateFailed
	} else if out.State != drive.SaveStateFailed && succeeded < len(req.Entries) {
		out.State = drive.SaveStatePartial
	}
	return out, nil
}

func (d *Driver) GetShareSave(context.Context, string, []drive.ShareEntry) (drive.ShareSaveOutcome, error) {
	return drive.ShareSaveOutcome{}, drive.NewShareError(drive.ShareErrUnsupported, "", "drime imports are synchronous")
}

func (d *Driver) findSavedEntry(ctx context.Context, destID, name string) (*drive.Entry, error) {
	entries, err := d.listChildren(ctx, destID)
	if err != nil {
		return nil, err
	}
	var latest *drive.Entry
	for i := range entries {
		if entries[i].Name == name {
			entry := entries[i]
			latest = &entry
		}
	}
	return latest, nil
}

func workspaceOf(entry drive.Entry, fallback string) string {
	raw, ok := drive.RawEntryExtra(entry.Extra).(map[string]any)
	if !ok {
		return fallback
	}
	switch value := raw["workspace_id"].(type) {
	case int64:
		return strconv.FormatInt(value, 10)
	case float64:
		return strconv.FormatInt(int64(value), 10)
	case string:
		if value != "" {
			return value
		}
	}
	return fallback
}

func shareFromLink(link *apiShareLink, entry drive.Entry, passcode string) drive.Share {
	expires := time.Time{}
	if link.ExpiresAt != nil {
		expires = parseAPITime(*link.ExpiresAt)
	}
	title := entry.Name
	if title == "" && link.Entry != nil {
		title = link.Entry.Name
	}
	nativeID := strconv.FormatInt(link.EntryID, 10)
	if link.EntryID == 0 && link.Entry != nil {
		nativeID = strconv.FormatInt(link.Entry.ID, 10)
	}
	return drive.Share{
		NativeID:  nativeID,
		LinkID:    link.Hash,
		URL:       "https://dri.me/" + link.Hash,
		Title:     title,
		Public:    link.Password == nil,
		Passcode:  passcode,
		ExpiresAt: expires,
		CreatedAt: parseAPITime(link.CreatedAt),
		UpdatedAt: parseAPITime(link.UpdatedAt),
	}
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
	default:
		return err
	}
}
