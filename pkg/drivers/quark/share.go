package quark

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Quark share links and save (转存) adapter.
//
// Verified request/response shapes live in charles-quark/pc/SHARE_SAVE_FLOW.md:
// link management is synchronous, a link's identity is pwd_id, its management
// identity is share_id, and every share entry carries a per-entry save ticket.

const (
	quarkSharePublic  = 1
	quarkSharePrivate = 2

	quarkSharePollInterval = 300 * time.Millisecond
	quarkSharePollTimeout  = 30 * time.Second
	quarkShareListPageSize = 50
	quarkShareMaxPages     = 20
)

// SharePolicy declares what Quark can do with links.
func (d *Driver) SharePolicy() drive.SharePolicy {
	return drive.SharePolicy{
		Passcode:   drive.PolicyOptional,
		Expiries:   drive.NeutralShareExpiries(),
		VisitLimit: true,
		Update:     true,
		List:       true,
		Selection:  true,
		// Quark fails an entry when the destination name exists; it cannot
		// auto-rename or overwrite during a save.
		Rename: drive.ShareRenameSupport{},
	}
}

type quarkShareTaskResp struct {
	respEnvelope
	Data quarkShareTask `json:"data"`
}

type quarkShareTask struct {
	TaskID   string           `json:"task_id"`
	TaskType int              `json:"task_type"`
	Status   int              `json:"status"`
	ShareID  string           `json:"share_id"`
	SaveAs   quarkShareSaveAs `json:"save_as"`
	ErrorMsg string           `json:"error_message"`
	Share    map[string]any   `json:"share"`
}

type quarkShareSaveAs struct {
	TopFids      []string `json:"save_as_top_fids"`
	SelectedFids []string `json:"save_as_select_top_fids"`
	ToPdirFid    string   `json:"to_pdir_fid"`
	ToPdirName   string   `json:"to_pdir_name"`
	SumNum       int      `json:"save_as_sum_num"`
	MinSaveSize  int64    `json:"min_save_file_size"`
	SearchExit   bool     `json:"search_exit"`
	IsPack       string   `json:"is_pack"`
}

type quarkShareCreateResp struct {
	respEnvelope
	Data struct {
		TaskID   string `json:"task_id"`
		TaskSync bool   `json:"task_sync"`
		TaskResp struct {
			Data quarkShareTask `json:"data"`
		} `json:"task_resp"`
	} `json:"data"`
}

type quarkSharePasswordResp struct {
	respEnvelope
	Data struct {
		Title     string `json:"title"`
		ShareID   string `json:"share_id"`
		PwdID     string `json:"pwd_id"`
		ShareURL  string `json:"share_url"`
		URLType   int    `json:"url_type"`
		Passcode  string `json:"passcode"`
		ExpiredAt int64  `json:"expired_at"`
		FileNum   int    `json:"file_num"`
		FirstFile struct {
			Size int64 `json:"size"`
		} `json:"first_file"`
	} `json:"data"`
}

type quarkShareListResp struct {
	respEnvelope
	Data struct {
		List []quarkShareItem `json:"list"`
	} `json:"data"`
	Metadata struct {
		Total int `json:"_total"`
	} `json:"metadata"`
}

type quarkShareItem struct {
	Title       string `json:"title"`
	ShareID     string `json:"share_id"`
	PwdID       string `json:"pwd_id"`
	ShareURL    string `json:"share_url"`
	URLType     int    `json:"url_type"`
	Passcode    string `json:"passcode"`
	ExpiredType int    `json:"expired_type"`
	ExpiredAt   int64  `json:"expired_at"`
	FileNum     int    `json:"file_num"`
	Size        int64  `json:"size"`
	PathInfo    string `json:"path_info"`
	AuditStatus int    `json:"audit_status"`
	Status      int    `json:"status"`
	VisitLimit  int    `json:"visit_user_limit"`
	VisitCount  int    `json:"visit_user_count"`
}

type quarkShareTokenResp struct {
	respEnvelope
	Data struct {
		Stoken    string `json:"stoken"`
		URLType   int    `json:"url_type"`
		ExpiredAt int64  `json:"expired_at"`
		Title     string `json:"title"`
		ShareType int    `json:"share_type"`
		Author    struct {
			NickName   string `json:"nick_name"`
			MemberType string `json:"member_type"`
			AvatarURL  string `json:"avatar_url"`
		} `json:"author"`
	} `json:"data"`
}

type quarkShareDetailResp struct {
	respEnvelope
	Data struct {
		IsOwner int               `json:"is_owner"`
		Share   quarkShareItem    `json:"share"`
		List    []quarkShareEntry `json:"list"`
	} `json:"data"`
	Metadata struct {
		Count int `json:"_count"`
		Total int `json:"_total"`
		Page  int `json:"_page"`
		Size  int `json:"_size"`
	} `json:"metadata"`
}

type quarkShareEntry struct {
	Fid        string `json:"fid"`
	FileName   string `json:"file_name"`
	PdirFid    string `json:"pdir_fid"`
	Dir        bool   `json:"dir"`
	Size       int64  `json:"size"`
	FormatType string `json:"format_type"`
	Ticket     string `json:"share_fid_token"`
}

type quarkShareSaveResp struct {
	respEnvelope
	Data struct {
		TaskID   string `json:"task_id"`
		TaskSync bool   `json:"task_sync"`
		TaskResp struct {
			Data quarkShareTask `json:"data"`
		} `json:"task_resp"`
	} `json:"data"`
}

type quarkFileInfoResp struct {
	respEnvelope
	Data struct {
		Fid      string `json:"fid"`
		FileName string `json:"file_name"`
		Size     int64  `json:"size"`
		PdirFid  string `json:"pdir_fid"`
		Dir      bool   `json:"dir"`
	} `json:"data"`
}

type quarkDownloadURLResp struct {
	respEnvelope
	Data []struct {
		Fid         string `json:"fid"`
		FileName    string `json:"file_name"`
		Size        int64  `json:"size"`
		DownloadURL string `json:"download_url"`
	} `json:"data"`
}

// CreateShare publishes a link and reads its identity back (the create task
// only returns share_id).
func (d *Driver) CreateShare(ctx context.Context, req drive.ShareCreateRequest) (drive.Share, error) {
	if len(req.Entries) == 0 {
		return drive.Share{}, drive.NewShareError(drive.ShareErrUnsupported, "", "quark: a share needs at least one entry")
	}
	fids := make([]string, 0, len(req.Entries))
	for _, entry := range req.Entries {
		fids = append(fids, entry.ID)
	}
	passcode := strings.TrimSpace(req.Passcode)
	urlType := quarkSharePublic
	if passcode != "" {
		urlType = quarkSharePrivate
	}
	body := map[string]any{
		"fid_list":           fids,
		"url_type":           urlType,
		"expired_type":       quarkExpiredType(req.Expiry),
		"support_error_code": []string{"41060"},
	}
	if title := strings.TrimSpace(req.Title); title != "" {
		body["title"] = title
	}
	if passcode != "" {
		body["passcode"] = passcode
	}
	if req.VisitLimit > 0 {
		body["visit_user_limit"] = req.VisitLimit
	}

	var created quarkShareCreateResp
	if err := d.cl.request(ctx, http.MethodPost, "/share", nil, body, &created); err != nil {
		return drive.Share{}, quarkShareError(err, drive.ShareErrProvider)
	}
	if err := checkShareEnvelope(nil, created.respEnvelope, drive.ShareErrProvider); err != nil {
		return drive.Share{}, err
	}
	task := created.Data.TaskResp.Data
	taskID := created.Data.TaskID
	if task.TaskID == "" {
		task.TaskID = taskID
	}
	if task.Status != 2 {
		polled, err := d.waitShareTask(ctx, task.TaskID)
		if err != nil {
			return drive.Share{}, err
		}
		task = polled
	}
	if task.ShareID == "" {
		return drive.Share{}, drive.NewShareError(drive.ShareErrProvider, "", "quark: share create returned no share id")
	}
	return d.shareByNativeID(ctx, task.ShareID)
}

func (d *Driver) waitShareTask(ctx context.Context, taskID string) (quarkShareTask, error) {
	timeout := d.sharePollTimeout
	if timeout <= 0 {
		timeout = quarkSharePollTimeout
	}
	interval := d.sharePollInterval
	if interval <= 0 {
		interval = quarkSharePollInterval
	}
	deadline := time.Now().Add(timeout)
	retry := 0
	for {
		if taskID == "" {
			return quarkShareTask{}, drive.NewShareError(drive.ShareErrProvider, "", "quark: share task has no id")
		}
		var resp quarkShareTaskResp
		err := d.cl.request(ctx, http.MethodGet, "/task", map[string]string{
			"task_id": taskID, "retry_index": strconv.Itoa(retry),
		}, nil, &resp)
		if err != nil {
			return quarkShareTask{}, quarkShareError(err, drive.ShareErrProvider)
		}
		if err := checkShareEnvelope(nil, resp.respEnvelope, drive.ShareErrProvider); err != nil {
			return quarkShareTask{}, err
		}
		switch resp.Data.Status {
		case 2:
			return resp.Data, nil
		case 3:
			return quarkShareTask{}, drive.NewShareError(drive.ShareErrProvider, "", "quark: share task failed")
		}
		retry++
		if time.Now().After(deadline) {
			return quarkShareTask{}, drive.NewShareError(drive.ShareErrTimeout, "", "quark: share task did not finish in time")
		}
		select {
		case <-ctx.Done():
			return quarkShareTask{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (d *Driver) shareByNativeID(ctx context.Context, shareID string) (drive.Share, error) {
	var resp quarkSharePasswordResp
	if err := d.cl.request(ctx, http.MethodPost, "/share/password", nil, map[string]any{"share_id": shareID}, &resp); err != nil {
		return drive.Share{}, quarkShareError(err, drive.ShareErrNotFound)
	}
	if err := checkShareEnvelope(nil, resp.respEnvelope, drive.ShareErrNotFound); err != nil {
		return drive.Share{}, err
	}
	return drive.Share{
		NativeID:   shareID,
		LinkID:     resp.Data.PwdID,
		URL:        resp.Data.ShareURL,
		Title:      resp.Data.Title,
		Public:     resp.Data.URLType == quarkSharePublic,
		Passcode:   resp.Data.Passcode,
		ExpiresAt:  quarkExpiredTime(resp.Data.ExpiredAt),
		EntryCount: resp.Data.FileNum,
		Size:       resp.Data.FirstFile.Size,
	}, nil
}

// ListShares pages through the acting account's links.
func (d *Driver) ListShares(ctx context.Context, req drive.ShareListRequest) (drive.SharePage, error) {
	page := 1
	if parsed, err := strconv.Atoi(strings.TrimSpace(req.Cursor)); err == nil && parsed > 0 {
		page = parsed
	}
	size := req.Limit
	if size <= 0 {
		size = quarkShareListPageSize
	}
	query := map[string]string{
		"_page":        strconv.Itoa(page),
		"_size":        strconv.Itoa(size),
		"_sort":        "created_at:desc",
		"_fetch_total": "1",
	}
	var resp quarkShareListResp
	if err := d.cl.request(ctx, http.MethodGet, "/share/mypage/detail", query, nil, &resp); err != nil {
		return drive.SharePage{}, quarkShareError(err, drive.ShareErrProvider)
	}
	if err := checkShareEnvelope(nil, resp.respEnvelope, drive.ShareErrProvider); err != nil {
		return drive.SharePage{}, err
	}
	shares := make([]drive.Share, 0, len(resp.Data.List))
	for _, item := range resp.Data.List {
		if !req.IncludeExpired && item.ExpiredAt > 0 && item.ExpiredAt < time.Now().UnixMilli() {
			continue
		}
		shares = append(shares, drive.Share{
			NativeID:   item.ShareID,
			LinkID:     item.PwdID,
			URL:        item.ShareURL,
			Title:      item.Title,
			Public:     item.URLType == quarkSharePublic,
			Passcode:   item.Passcode,
			ExpiresAt:  quarkExpiredTime(item.ExpiredAt),
			VisitLimit: item.VisitLimit,
			VisitCount: item.VisitCount,
			EntryCount: item.FileNum,
			Size:       item.Size,
		})
	}
	next := ""
	if resp.Metadata.Total > page*size {
		next = strconv.Itoa(page + 1)
	}
	return drive.SharePage{Shares: shares, Next: next}, nil
}

// GetShare resolves a native id (32 hex) or a link id (12 alphanumeric).
func (d *Driver) GetShare(ctx context.Context, id string) (drive.Share, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return drive.Share{}, drive.NewShareError(drive.ShareErrNotFound, "", "quark: link id is required")
	}
	if len(id) == 32 {
		return d.shareByNativeID(ctx, id)
	}
	cursor := ""
	for page := 0; page < quarkShareMaxPages; page++ {
		result, err := d.ListShares(ctx, drive.ShareListRequest{Cursor: cursor, Limit: quarkShareListPageSize, IncludeExpired: true})
		if err != nil {
			return drive.Share{}, err
		}
		for _, share := range result.Shares {
			if share.LinkID == id || share.NativeID == id || share.URL == id {
				return share, nil
			}
		}
		if result.Next == "" {
			break
		}
		cursor = result.Next
	}
	return drive.Share{}, drive.NewShareError(drive.ShareErrNotFound, "", "quark: no such link")
}

// UpdateShare changes a link's policy and returns the fresh metadata.
func (d *Driver) UpdateShare(ctx context.Context, req drive.ShareUpdateRequest) (drive.Share, error) {
	share, err := d.GetShare(ctx, req.ID)
	if err != nil {
		return drive.Share{}, err
	}
	body := map[string]any{"pwd_id": share.LinkID}
	if req.Title != nil {
		body["title"] = *req.Title
	}
	if req.Expiry != nil {
		body["expired_type"] = quarkExpiredType(*req.Expiry)
	}
	if req.Passcode != nil {
		body["passcode"] = *req.Passcode
	}
	if req.Public != nil {
		if *req.Public {
			body["url_type"] = quarkSharePublic
		} else {
			body["url_type"] = quarkSharePrivate
		}
	}
	var updated struct{ respEnvelope }
	if err := d.cl.request(ctx, http.MethodPost, "/share/update", nil, body, &updated); err != nil {
		return drive.Share{}, quarkShareError(err, drive.ShareErrProvider)
	}
	if err := checkShareEnvelope(nil, updated.respEnvelope, drive.ShareErrProvider); err != nil {
		return drive.Share{}, err
	}
	return d.shareByNativeID(ctx, share.NativeID)
}

// DeleteShare removes one link by its management id.
func (d *Driver) DeleteShare(ctx context.Context, id string) error {
	share, err := d.GetShare(ctx, id)
	if err != nil {
		return err
	}
	var deleted struct{ respEnvelope }
	if err := d.cl.request(ctx, http.MethodPost, "/share/delete", nil, map[string]any{"share_ids": []string{share.NativeID}}, &deleted); err != nil {
		return quarkShareError(err, drive.ShareErrNotFound)
	}
	if err := checkShareEnvelope(nil, deleted.respEnvelope, drive.ShareErrNotFound); err != nil {
		return err
	}
	return nil
}

// OpenShare exchanges a link for a session token.
func (d *Driver) OpenShare(ctx context.Context, link drive.ShareLink) (drive.ShareSession, error) {
	body := map[string]any{
		"pwd_id":                            link.LinkID,
		"passcode":                          link.Passcode,
		"support_visit_limit_private_share": true,
	}
	var resp quarkShareTokenResp
	if err := d.cl.request(ctx, http.MethodPost, "/share/sharepage/token", nil, body, &resp); err != nil {
		return drive.ShareSession{}, quarkShareError(err, drive.ShareErrProvider)
	}
	if err := checkShareEnvelope(nil, resp.respEnvelope, drive.ShareErrProvider); err != nil {
		return drive.ShareSession{}, err
	}
	if resp.Data.Stoken == "" {
		return drive.ShareSession{}, drive.NewShareError(drive.ShareErrProvider, "", "quark: share token missing")
	}
	return drive.ShareSession{
		Link:         link,
		SessionToken: resp.Data.Stoken,
		ExpiresAt:    quarkExpiredTime(resp.Data.ExpiredAt),
		Share: drive.Share{
			LinkID:    link.LinkID,
			URL:       link.URL,
			Title:     resp.Data.Title,
			Owner:     drive.ShareOwner{Name: resp.Data.Author.NickName, MemberType: resp.Data.Author.MemberType, AvatarURL: resp.Data.Author.AvatarURL},
			Public:    resp.Data.URLType == quarkSharePublic,
			ExpiresAt: quarkExpiredTime(resp.Data.ExpiredAt),
		},
	}, nil
}

// ListShareEntries lists one share directory. The cursor is the next page
// number; an empty cursor starts at page 1.
func (d *Driver) ListShareEntries(ctx context.Context, session drive.ShareSession, dir drive.SharePath, cursor string) (drive.ShareEntryPage, error) {
	pdir := strings.TrimSpace(dir.ID)
	if pdir == "" {
		pdir = "0"
	}
	page := 1
	if parsed, err := strconv.Atoi(strings.TrimSpace(cursor)); err == nil && parsed > 0 {
		page = parsed
	}
	query := map[string]string{
		"pwd_id":            session.Link.LinkID,
		"stoken":            session.SessionToken,
		"pdir_fid":          pdir,
		"force":             "0",
		"_page":             strconv.Itoa(page),
		"_size":             "100",
		"ver":               "2",
		"_fetch_total":      "1",
		"_sort":             "file_type:asc,file_name:asc",
		"fetch_update_flag": "1",
	}
	if pdir == "0" {
		query["_fetch_banner"] = "1"
		query["_fetch_share"] = "1"
	}
	var resp quarkShareDetailResp
	if err := d.cl.request(ctx, http.MethodGet, "/share/sharepage/detail", query, nil, &resp); err != nil {
		return drive.ShareEntryPage{}, quarkShareError(err, drive.ShareErrProvider)
	}
	if err := checkShareEnvelope(nil, resp.respEnvelope, drive.ShareErrProvider); err != nil {
		return drive.ShareEntryPage{}, err
	}
	entries := make([]drive.ShareEntry, 0, len(resp.Data.List))
	for _, item := range resp.Data.List {
		entries = append(entries, drive.ShareEntry{
			Entry: drive.Entry{
				ID:       item.Fid,
				ParentID: item.PdirFid,
				Name:     item.FileName,
				IsDir:    item.Dir,
				Size:     item.Size,
			},
			Ticket: item.Ticket,
			RootID: pdir,
		})
	}
	next := ""
	if resp.Metadata.Size > 0 && resp.Metadata.Total > page*resp.Metadata.Size {
		next = strconv.Itoa(page + 1)
	}
	return drive.ShareEntryPage{Entries: entries, Next: next}, nil
}

// SaveShareEntries copies share entries into the user's drive (转存).
func (d *Driver) SaveShareEntries(ctx context.Context, req drive.ShareSaveRequest) (drive.ShareSaveOutcome, error) {
	if len(req.Entries) == 0 {
		return drive.ShareSaveOutcome{}, drive.NewShareError(drive.ShareErrUnsupported, "", "quark: no entry to save")
	}
	fids := make([]string, 0, len(req.Entries))
	tickets := make([]string, 0, len(req.Entries))
	pdir := "0"
	for _, entry := range req.Entries {
		fids = append(fids, entry.ID)
		tickets = append(tickets, entry.Ticket)
		if entry.RootID != "" {
			pdir = entry.RootID
		}
	}
	body := map[string]any{
		"fid_list":       fids,
		"fid_token_list": tickets,
		"to_pdir_fid":    req.Dest.Entry.ID,
		"pwd_id":         req.Session.Link.LinkID,
		"stoken":         req.Session.SessionToken,
		"pdir_fid":       pdir,
		"scene":          "link",
		"exclude_fids":   []string{},
	}
	var resp quarkShareSaveResp
	if err := d.cl.request(ctx, http.MethodPost, "/share/sharepage/save", nil, body, &resp); err != nil {
		return drive.ShareSaveOutcome{}, quarkShareError(err, drive.ShareErrProvider)
	}
	if err := checkShareEnvelope(nil, resp.respEnvelope, drive.ShareErrProvider); err != nil {
		return drive.ShareSaveOutcome{}, err
	}
	task := resp.Data.TaskResp.Data
	if task.TaskID == "" {
		task.TaskID = resp.Data.TaskID
	}
	if task.Status == 2 {
		return d.shareSaveOutcome(ctx, task, req.Entries)
	}
	d.rememberShareSave(task.TaskID, req.Entries)
	return drive.ShareSaveOutcome{Handle: task.TaskID, State: drive.SaveStateRunning}, nil
}

// rememberShareSave records the entries a provider save was asked to copy.
// Quark's task response lists only successful destination FIDs, so the request
// entries are the only way to turn a partial asynchronous save into per-entry
// results. The binding is in-memory: after a restart the entry list is unknown,
// and Core reconciles the provider's successes against the selection it lists
// again.
func (d *Driver) rememberShareSave(handle string, entries []drive.ShareEntry) {
	if handle == "" || len(entries) == 0 {
		return
	}
	copied := append([]drive.ShareEntry(nil), entries...)
	d.shareSaveMu.Lock()
	defer d.shareSaveMu.Unlock()
	if d.shareSaves == nil {
		d.shareSaves = map[string][]drive.ShareEntry{}
	}
	if len(d.shareSaves) >= 256 {
		for key := range d.shareSaves {
			delete(d.shareSaves, key)
			break
		}
	}
	d.shareSaves[handle] = copied
}

func (d *Driver) rememberedShareSave(handle string) []drive.ShareEntry {
	d.shareSaveMu.Lock()
	defer d.shareSaveMu.Unlock()
	entries := d.shareSaves[handle]
	if len(entries) == 0 {
		return nil
	}
	return append([]drive.ShareEntry(nil), entries...)
}

func (d *Driver) forgetShareSave(handle string) {
	d.shareSaveMu.Lock()
	delete(d.shareSaves, handle)
	d.shareSaveMu.Unlock()
}

// GetShareSave polls a provider save by its task id.
func (d *Driver) GetShareSave(ctx context.Context, handle string, entries []drive.ShareEntry) (drive.ShareSaveOutcome, error) {
	if strings.TrimSpace(handle) == "" {
		return drive.ShareSaveOutcome{}, drive.NewShareError(drive.ShareErrNotFound, "", "quark: no save handle")
	}
	handle = strings.TrimSpace(handle)
	if !isQuarkTaskID(handle) {
		return drive.ShareSaveOutcome{Handle: handle, State: drive.SaveStateRunning}, nil
	}
	var resp quarkShareTaskResp
	err := d.cl.request(ctx, http.MethodGet, "/task", map[string]string{"task_id": handle, "retry_index": "0"}, nil, &resp)
	if err != nil {
		// A transport, timeout, or throttling failure says nothing about
		// whether the provider task still exists. Only an explicit task-missing
		// code may tell Core the handle is gone; everything else stays a
		// provider error so a submitted save is not repeated.
		return drive.ShareSaveOutcome{}, quarkShareError(err, drive.ShareErrProvider)
	}
	if err := checkShareEnvelope(nil, resp.respEnvelope, drive.ShareErrProvider); err != nil {
		return drive.ShareSaveOutcome{}, err
	}
	switch resp.Data.Status {
	case 2:
		if len(entries) == 0 {
			entries = d.rememberedShareSave(handle)
		}
		d.forgetShareSave(handle)
		return d.shareSaveOutcome(ctx, resp.Data, entries)
	case 3:
		d.forgetShareSave(handle)
		return drive.ShareSaveOutcome{Handle: handle, State: drive.SaveStateFailed}, nil
	default:
		return drive.ShareSaveOutcome{Handle: handle, State: drive.SaveStateRunning}, nil
	}
}

// ShareDownloadTargets hands out signed download URLs for share content without
// saving it. The client's UA is the PC desktop client UA, which is what the
// provider requires for this path.
func (d *Driver) ShareDownloadTargets(ctx context.Context, req drive.ShareFetchRequest) ([]drive.DownloadTarget, error) {
	fids := make([]string, 0, len(req.Entries))
	tickets := make([]string, 0, len(req.Entries))
	for _, entry := range req.Entries {
		fids = append(fids, entry.ID)
		tickets = append(tickets, entry.Ticket)
	}
	body := map[string]any{
		"fids":               fids,
		"fids_token":         tickets,
		"pwd_id":             req.Session.Link.LinkID,
		"stoken":             req.Session.SessionToken,
		"support_error_code": []string{"41060"},
	}
	var resp quarkDownloadURLResp
	// The signed CDN URL is bound to the cookie that requested it and to the
	// browser-like headers the official client sends. The shared transfer layer
	// only sees DownloadTarget.Headers, so the binding values must travel with
	// the URL or the CDN answers 403.
	cookieSnapshot := d.cl.cookieValue()
	if err := d.cl.requestWithCookie(ctx, http.MethodPost, "/file/download", map[string]string{"where_entry": "share"}, body, &resp, cookieSnapshot); err != nil {
		return nil, quarkShareError(err, drive.ShareErrProvider)
	}
	if err := checkShareEnvelope(nil, resp.respEnvelope, drive.ShareErrProvider); err != nil {
		return nil, err
	}
	headers := shareDownloadHeaders(cookieSnapshot)
	byID := make(map[string]drive.DownloadTarget, len(resp.Data))
	for _, item := range resp.Data {
		if item.DownloadURL == "" {
			continue
		}
		byID[item.Fid] = drive.DownloadTarget{URL: item.DownloadURL, Size: item.Size, Headers: headers}
	}
	targets := make([]drive.DownloadTarget, 0, len(req.Entries))
	for _, entry := range req.Entries {
		target, ok := byID[entry.ID]
		if !ok {
			return nil, drive.NewShareError(drive.ShareErrProvider, "", fmt.Sprintf("quark: no download URL for %s", entry.Name))
		}
		targets = append(targets, target)
	}
	return targets, nil
}

// shareDownloadHeaders mirrors the headers the driver's own CDN fetch sends
// (doDownload): Quark binds a signed URL to the cookie that requested it and
// rejects browser-less requests. The transfer layer replays these verbatim.
func shareDownloadHeaders(cookie string) map[string]string {
	return map[string]string{
		"Cookie":                    cookie,
		"User-Agent":                downloadChromeUA,
		"Referer":                   defaultReferer + "/",
		"Accept":                    "*/*",
		"Accept-Language":           "zh-CN,zh;q=0.9",
		"Cache-Control":             "no-cache",
		"Pragma":                    "no-cache",
		"Priority":                  "u=0, i",
		"Sec-Ch-Ua":                 `"Chromium";v="146", "Google Chrome";v="146", "Not_A Brand";v="24"`,
		"Sec-Ch-Ua-Mobile":          "?0",
		"Sec-Ch-Ua-Platform":        `"Windows"`,
		"Sec-Fetch-Site":            "same-site",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Dest":            "iframe",
		"Sec-Fetch-User":            "?1",
		"Upgrade-Insecure-Requests": "1",
	}
}

// shareSaveOutcome maps a finished provider save onto per-entry outcomes.
func (d *Driver) shareSaveOutcome(ctx context.Context, task quarkShareTask, entries []drive.ShareEntry) (drive.ShareSaveOutcome, error) {
	top := task.SaveAs.TopFids
	if len(top) == 0 {
		top = task.SaveAs.SelectedFids
	}
	items := make([]drive.ShareSaveItem, 0, len(top))
	if len(entries) > 0 {
		items = make([]drive.ShareSaveItem, 0, len(entries))
		for index, entry := range entries {
			item := drive.ShareSaveItem{SourceID: entry.ID, SourceName: entry.Name, SourceSize: entry.Size}
			if index < len(top) {
				saved := drive.Entry{ID: top[index], Name: entry.Name, ParentID: task.SaveAs.ToPdirFid, Size: entry.Size}
				if info, err := d.shareFileInfo(ctx, top[index]); err == nil {
					if info.Data.FileName != "" {
						saved.Name = info.Data.FileName
					}
					if info.Data.Size > 0 {
						saved.Size = info.Data.Size
					}
				}
				item.Saved = &saved
			} else {
				item.Error = drive.NewShareError(drive.ShareErrProvider, "", "quark: entry was not saved")
			}
			items = append(items, item)
		}
	} else {
		for _, fid := range top {
			saved := drive.Entry{ID: fid, ParentID: task.SaveAs.ToPdirFid}
			if info, err := d.shareFileInfo(ctx, fid); err == nil {
				saved.Name, saved.Size = info.Data.FileName, info.Data.Size
			}
			items = append(items, drive.ShareSaveItem{SourceID: fid, SourceName: saved.Name, SourceSize: saved.Size, Saved: &saved})
		}
	}
	state := drive.SaveStateSucceeded
	failed := 0
	for _, item := range items {
		if item.Error != nil {
			failed++
		}
	}
	switch {
	case len(items) == 0 || failed == len(items):
		state = drive.SaveStateFailed
	case failed > 0:
		state = drive.SaveStatePartial
	}
	return drive.ShareSaveOutcome{Handle: task.TaskID, State: state, Progress: 1, Items: items}, nil
}

func (d *Driver) shareFileInfo(ctx context.Context, fid string) (quarkFileInfoResp, error) {
	var resp quarkFileInfoResp
	if err := d.cl.request(ctx, http.MethodGet, "/file/info", map[string]string{"fid": fid}, nil, &resp); err != nil {
		return quarkFileInfoResp{}, err
	}
	if err := checkShareEnvelope(nil, resp.respEnvelope, drive.ShareErrNotFound); err != nil {
		return quarkFileInfoResp{}, err
	}
	return resp, nil
}

// quarkExpiredType maps a neutral expiry onto Quark's enum.
func quarkExpiredType(expiry time.Duration) int {
	switch expiry {
	case drive.ShareExpiryDay:
		return 2
	case drive.ShareExpiryWeek:
		return 3
	case drive.ShareExpiryMonth:
		return 4
	default:
		return 1
	}
}

func quarkExpiredTime(milliseconds int64) time.Time {
	if milliseconds <= 0 {
		return time.Time{}
	}
	if milliseconds >= 4102416000000 {
		// Provider's "permanent" sentinel.
		return time.Time{}
	}
	return time.UnixMilli(milliseconds).UTC()
}

func isQuarkTaskID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// checkShareEnvelope turns a provider envelope into a neutral error. The
// transport returns HTTP 200 for business errors, so callers must inspect the
// envelope's status/code.
func checkShareEnvelope(err error, env respEnvelope, fallback drive.ShareErrorCode) error {
	if err != nil {
		return quarkShareError(err, fallback)
	}
	if env.Status < 400 && env.Code == 0 {
		return nil
	}
	return quarkShareError(&APIError{Status: env.Status, Code: env.Code, Message: env.Message}, fallback)
}

// quarkShareError maps provider codes onto the neutral share vocabulary.
func quarkShareError(err error, fallback drive.ShareErrorCode) error {
	if err == nil {
		return nil
	}
	var api *APIError
	if errors.As(err, &api) {
		code := fallback
		message := api.Message
		switch api.Code {
		case 14013, 23001, 41006:
			code = drive.ShareErrNotFound
		case 41007:
			code = drive.ShareErrPasscodeWrong
			// Passcode errors surface as the neutral code only: the provider's
			// own wording is not a stable API and must not become the message
			// a caller or script sees.
			message = ""
		case 41008:
			code = drive.ShareErrPasscodeRequired
			message = ""
		case 41017:
			code = drive.ShareErrOwnSave
		case 41019:
			code = drive.ShareErrExpired
		case 41020:
			code = drive.ShareErrTicketInvalid
		case 45058:
			code = drive.ShareErrVisitLimit
		case 32003, 41014, 41035:
			code = drive.ShareErrQuota
		}
		return drive.NewShareError(code, strconv.Itoa(api.Code), message)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return drive.NewShareError(drive.ShareErrTimeout, "", err.Error())
	}
	if errors.Is(err, drive.ErrNotFound) {
		return drive.NewShareError(drive.ShareErrNotFound, "", err.Error())
	}
	return drive.NewShareError(fallback, "", err.Error())
}
