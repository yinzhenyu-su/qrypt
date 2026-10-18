package quark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Quark offline adapter. It implements the provider-neutral offline contracts
// against Quark's `offline/download/parse` + `offline/download/submit` flow and
// its `task` observation API. The decrypted reference is
// openspec/changes/add-offline-download/design.md (appendix A).

const (
	// apiVersionOffline is the api_ver the offline endpoints require.
	apiVersionOffline = "2"
	// parseModeNormal is the magnet/BT parse mode; accelerated downloads use
	// parseModeSpeedup for submit.
	parseModeNormal  = 0
	parseModeSpeedup = 22
)

// offdlResultFile is one file the provider reports for a finished offline task.
type offdlResultFile struct {
	Fid      string      `json:"fid"`
	FileName string      `json:"file_name"`
	PdirFid  string      `json:"pdir_fid"`
	Size     json.Number `json:"size"`
}

// offflineParseFile is one parsed file from the parse step.
type offlineParseFile struct {
	FileNo      int         `json:"file_no"`
	FileName    string      `json:"file_name"`
	Size        json.Number `json:"size"`
	Path        string      `json:"path"`
	TorrentName string      `json:"torrent_name"`
	FileDirs    []string    `json:"file_dirs"`
	FormatType  string      `json:"format_type"`
}

type offlineParseDTO struct {
	Token        string             `json:"token"`
	Finished     bool               `json:"finished"`
	Files        []offlineParseFile `json:"files"`
	Title        string             `json:"title"`
	TotalSize    json.Number        `json:"total_size"`
	ResourceInfo struct {
		DownloadID         string `json:"download_id"`
		DownloadTaskFinish int    `json:"download_task_finish"`
		DetailErrorCode    int    `json:"detail_error_code"`
		CanRetry           int    `json:"can_retry"`
	} `json:"resource_info"`
}

type offlineParseResp struct {
	respEnvelope
	Data offlineParseDTO `json:"data"`
	offlineParseDTO
}

func (r offlineParseResp) parse() offlineParseDTO {
	if r.Data.Token != "" || r.Data.Finished || len(r.Data.Files) > 0 {
		return r.Data
	}
	return r.offlineParseDTO
}

type offlineSubmitDTO struct {
	TaskID        string `json:"task_id"`
	Fid           string `json:"fid"`
	DirFid        string `json:"dir_fid"`
	SpeedupDLInfo struct {
		SpeedupSession string `json:"speedup_session"`
	} `json:"speedup_dl_info"`
}

type offlineSubmitResp struct {
	respEnvelope
	Data offlineSubmitDTO `json:"data"`
	offlineSubmitDTO
}

func (r offlineSubmitResp) submit() offlineSubmitDTO {
	if r.Data.TaskID != "" {
		return r.Data
	}
	return r.offlineSubmitDTO
}

type offlineTaskDTO struct {
	TaskID      string `json:"task_id"`
	TaskTitle   string `json:"task_title"`
	TaskType    int    `json:"task_type"`
	TaskStatus  int    `json:"status"`
	Progress    int    `json:"progress"`
	CreatedAt   int64  `json:"created_at"`
	FinishedAt  int64  `json:"finished_at"`
	OffdlResult struct {
		List []offdlResultFile `json:"list"`
	} `json:"offdl_result"`
}

type offlineTaskResp struct {
	respEnvelope
	Data offlineTaskDTO
}

// UnmarshalJSON accepts both the data-wrapped and flat task response shapes.
// The envelope's HTTP-ish "status" and the task's 0..4 "status" share a key,
// so the two are decoded separately instead of by struct embedding.
func (r *offlineTaskResp) UnmarshalJSON(data []byte) error {
	var envelope respEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	r.Status, r.Code, r.Message = envelope.Status, envelope.Code, envelope.Message
	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}
	if len(wrapper.Data) > 0 && string(wrapper.Data) != "null" {
		var dto offlineTaskDTO
		if err := json.Unmarshal(wrapper.Data, &dto); err != nil {
			return err
		}
		if dto.TaskID != "" || dto.TaskStatus != 0 || len(dto.OffdlResult.List) > 0 {
			r.Data = dto
			return nil
		}
	}
	var flat offlineTaskDTO
	if err := json.Unmarshal(data, &flat); err != nil {
		return err
	}
	r.Data = flat
	return nil
}

func (r offlineTaskResp) task() offlineTaskDTO {
	return r.Data
}

// offlineSystemPathResp is the `/file/system_path` answer. The provider returns
// the directory as `data.path_files[]` (the last entry is the target) and only
// some versions also echo a top-level fid, so both shapes are accepted.
type offlineSystemPathResp struct {
	respEnvelope
	Data struct {
		Fid       string `json:"fid"`
		PathFiles []struct {
			Fid string `json:"fid"`
		} `json:"path_files"`
	} `json:"data"`
}

// dirFid is the resolved directory fid, preferring the path_files list.
func (r offlineSystemPathResp) dirFid() string {
	for i := len(r.Data.PathFiles) - 1; i >= 0; i-- {
		if fid := strings.TrimSpace(r.Data.PathFiles[i].Fid); fid != "" {
			return fid
		}
	}
	return strings.TrimSpace(r.Data.Fid)
}

// OfflineSourceKinds reports the normalized source kinds the Quark adapter
// accepts: magnet, HTTP(S), and FTP URLs.
func (d *Driver) OfflineSourceKinds() []drive.OfflineSourceKind {
	return []drive.OfflineSourceKind{drive.OfflineSourceMagnet, drive.OfflineSourceHTTP, drive.OfflineSourceFTP}
}

// CreateOfflineDownload runs the unified parse + submit flow. It persists no
// token (the token lives only for this call) and returns the provider task id
// with its first observation.
func (d *Driver) CreateOfflineDownload(ctx context.Context, req drive.OfflineDownloadRequest) (drive.OfflineDownload, error) {
	parsed, err := d.parseOffline(ctx, req, req.Accelerated)
	if err != nil {
		return drive.OfflineDownload{}, err
	}
	if !parsed.Finished {
		return drive.OfflineDownload{}, drive.NewOfflineError(drive.OfflineErrProviderUnavailable, "", "quark: resource parse did not finish")
	}
	files := selectQuarkFiles(parsed.Files, req.Selection)
	dirFid, err := d.offlineStagingDir(ctx, req)
	if err != nil {
		return drive.OfflineDownload{}, err
	}
	result, err := d.submitOffline(ctx, parsed.Token, dirFid, req, files, req.Accelerated)
	if err != nil {
		return drive.OfflineDownload{}, err
	}
	if result.SpeedupDLInfo.SpeedupSession != "" {
		d.rememberOfflineSession(result.TaskID, result.SpeedupDLInfo.SpeedupSession)
	}
	observation := drive.OfflineDownload{
		ID:          result.TaskID,
		State:       drive.OfflineStateQueued,
		Destination: dirFid,
		RemoteID:    result.Fid,
	}
	return observation, nil
}

// GetOfflineDownload observes one provider task by task_id.
func (d *Driver) GetOfflineDownload(ctx context.Context, id string) (drive.OfflineDownload, error) {
	var resp offlineTaskResp
	err := d.cl.request(ctx, http.MethodGet, "/task", map[string]string{"task_id": id}, nil, &resp)
	if err != nil {
		if isOfflineTaskMissing(err) {
			return drive.OfflineDownload{}, drive.NewOfflineError(drive.OfflineErrProviderTaskMissing, "14013", "quark: offline task not found")
		}
		return drive.OfflineDownload{}, err
	}
	if resp.Code != 0 {
		if resp.Code == 14013 {
			return drive.OfflineDownload{}, drive.NewOfflineError(drive.OfflineErrProviderTaskMissing, "14013", resp.Message)
		}
		return drive.OfflineDownload{}, offlineErrorFromCode(resp.Code, resp.Message)
	}
	task := resp.task()
	observation := drive.OfflineDownload{
		ID:    id,
		State: quarkOfflineState(task.TaskStatus),
		Progress: drive.OfflineProgress{
			Percent: float64(task.Progress) / 10,
		},
	}
	for i, file := range task.OffdlResult.List {
		observation.Files = append(observation.Files, drive.OfflineFile{
			Index:    i,
			Name:     file.FileName,
			Size:     jsonInt64(file.Size),
			RemoteID: file.Fid,
		})
	}
	switch observation.State {
	case drive.OfflineStateSucceeded:
		observation.Progress.Percent = 100
	case drive.OfflineStateFailed:
		observation.ErrorCode = string(drive.OfflineErrProvider)
		observation.Error = fmt.Sprintf("quark: offline task failed (status %d)", task.TaskStatus)
	}
	return observation, nil
}

// CancelOfflineDownload deletes the provider task (op_type 0).
func (d *Driver) CancelOfflineDownload(ctx context.Context, id string) error {
	return d.offlineTaskOp(ctx, id, 0)
}

// RetryOfflineDownload re-queues the provider task in place (op_type 1).
func (d *Driver) RetryOfflineDownload(ctx context.Context, id string) error {
	return d.offlineTaskOp(ctx, id, 1)
}

func (d *Driver) offlineTaskOp(ctx context.Context, id string, opType int) error {
	body := map[string]any{
		"task_ids": []string{id},
		"op_type":  opType,
	}
	var resp respEnvelope
	if err := d.cl.request(ctx, http.MethodPost, "/task/status", nil, body, &resp); err != nil {
		if isOfflineTaskMissing(err) {
			return drive.NewOfflineError(drive.OfflineErrProviderTaskMissing, "14013", "quark: offline task not found")
		}
		return err
	}
	if resp.Code != 0 && resp.Code != 23004 {
		return offlineErrorFromCode(resp.Code, resp.Message)
	}
	if opType == 0 {
		d.forgetOfflineSession(id)
	}
	return nil
}

// InspectOfflineDownload runs the parse step only and reports the parsed files
// without creating a task or persisting the token.
func (d *Driver) InspectOfflineDownload(ctx context.Context, req drive.OfflineDownloadRequest) (drive.OfflinePreview, error) {
	parsed, err := d.parseOffline(ctx, req, false)
	if err != nil {
		return drive.OfflinePreview{}, err
	}
	preview := drive.OfflinePreview{
		Source:      req.Source,
		SourceKind:  req.SourceKind,
		Title:       parsed.Title,
		TotalSize:   jsonInt64(parsed.TotalSize),
		InstantSave: parsed.ResourceInfo.DownloadTaskFinish == 1,
	}
	for i, file := range parsed.Files {
		preview.Files = append(preview.Files, drive.OfflineFile{
			Index: i,
			Name:  quarkFileName(file),
			Size:  jsonInt64(file.Size),
		})
	}
	return preview, nil
}

// OfflineDownloadURL resolves a download URL for staged fids. An accelerated
// request uses the submit session; when no session exists it degrades to the
// normal URL the caller requests after the provider task completes.
func (d *Driver) OfflineDownloadURL(ctx context.Context, req drive.OfflineFetchRequest) (drive.OfflineDownloadTarget, error) {
	fids := make([]string, 0, len(req.Files))
	for _, file := range req.Files {
		if file.RemoteID != "" {
			fids = append(fids, file.RemoteID)
		}
	}
	if len(fids) == 0 {
		return drive.OfflineDownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "quark: no staged files to download")
	}
	// Mirror the driver's proven download call: a plain fids request through a
	// fixed cookie snapshot (the signed URL is bound to that cookie). The
	// accelerated path adds the session; a missing session makes the caller
	// request the normal URL instead.
	body := map[string]any{"fids": fids}
	if req.Accelerated {
		session, ok := d.offlineSession(req.ID)
		if !ok || session == "" {
			return drive.OfflineDownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "quark: no accelerated session")
		}
		body["parse_mode"] = parseModeSpeedup
		body["speedup_session"] = session
	}
	var resp downResp
	cookieSnapshot := d.cl.cookieValue()
	if err := d.cl.requestWithCookie(ctx, http.MethodPost, "/file/download", nil, body, &resp, cookieSnapshot); err != nil {
		return drive.OfflineDownloadTarget{}, err
	}
	if resp.Code != 0 {
		return drive.OfflineDownloadTarget{}, offlineErrorFromCode(resp.Code, resp.Message)
	}
	if len(resp.Data) == 0 || resp.Data[0].DownloadURL == "" {
		return drive.OfflineDownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "quark: provider returned no download URL")
	}
	// The signed CDN URL is bound to the cookie that requested it and to the
	// client's browser-like headers; without them the CDN answers 403.
	return drive.OfflineDownloadTarget{
		URL:       resp.Data[0].DownloadURL,
		ExpiresAt: offlineURLExpiry(),
		Headers: map[string]string{
			"Cookie":          cookieSnapshot,
			"User-Agent":      downloadChromeUA,
			"Referer":         defaultReferer + "/",
			"Accept":          "*/*",
			"Accept-Language": "zh-CN,zh;q=0.9",
		},
	}, nil
}

// offlineURLExpiry is the conservative lifetime of a Quark signed download URL
// (~30 minutes). The transfer refreshes the URL when it is missing or expired.
func offlineURLExpiry() time.Time {
	return time.Now().Add(30 * time.Minute)
}

// parseOffline sends the wg-encrypted parse request.
func (d *Driver) parseOffline(ctx context.Context, req drive.OfflineDownloadRequest, accelerated bool) (offlineParseDTO, error) {
	body := map[string]any{
		"url":             req.Source,
		"fid":             "",
		"title":           req.Title,
		"parse_mode":      parseModeNormal,
		"cookie":          "",
		"referer":         "",
		"page_url":        "",
		"entry":           "download",
		"req_info":        map[string]any{"method": "", "body": "", "is_multipart": false},
		"conflict_mode":   4,
		"save_path":       "",
		"auto_download":   false,
		"support_v2_play": true,
	}
	if accelerated {
		body["parse_mode"] = parseModeSpeedup
	}
	var resp offlineParseResp
	if err := d.cl.request(ctx, http.MethodPost, "/offline/download/parse", map[string]string{"api_ver": apiVersionOffline}, wgBody{payload: body}, &resp); err != nil {
		return offlineParseDTO{}, err
	}
	if resp.Code != 0 {
		return offlineParseDTO{}, offlineErrorFromCode(resp.Code, resp.Message)
	}
	return resp.parse(), nil
}

// submitOffline sends the wg-encrypted submit request.
func (d *Driver) submitOffline(ctx context.Context, token, pdirFid string, req drive.OfflineDownloadRequest, files []int, accelerated bool) (offlineSubmitDTO, error) {
	parseMode := parseModeNormal
	if accelerated {
		parseMode = parseModeSpeedup
	}
	body := map[string]any{
		"token":          token,
		"entry":          "download",
		"parse_mode":     parseMode,
		"pdir_fid":       pdirFid,
		"auto_select":    false,
		"selected_files": files,
		"title":          req.Title,
	}
	var resp offlineSubmitResp
	if err := d.cl.request(ctx, http.MethodPost, "/offline/download/submit", nil, wgBody{payload: body}, &resp); err != nil {
		return offlineSubmitDTO{}, err
	}
	if resp.Code != 0 {
		return offlineSubmitDTO{}, offlineErrorFromCode(resp.Code, resp.Message)
	}
	result := resp.submit()
	if result.TaskID == "" {
		return offlineSubmitDTO{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "quark: submit returned no task id")
	}
	return result, nil
}

// offlineStagingDir resolves the submit directory fid. An empty destination
// uses the provider's system directory for the scene.
func (d *Driver) offlineStagingDir(ctx context.Context, req drive.OfflineDownloadRequest) (string, error) {
	// Prefer the directory identity the caller already resolved; Quark's
	// listing is eventually consistent, so re-resolving a just-created
	// directory by path can fail.
	if id := strings.TrimSpace(req.DestinationID); id != "" {
		return id, nil
	}
	destination := strings.TrimSpace(req.Destination)
	if destination != "" && destination != "/" {
		fid, err := d.ResolvePath(ctx, destination)
		if err != nil {
			return "", err
		}
		return fid, nil
	}
	if destination == "/" {
		return d.rootID, nil
	}
	scene := "offline"
	if req.SourceKind == drive.OfflineSourceMagnet {
		scene = "offline_bt"
	}
	body := map[string]any{"scene": scene, "init": true}
	var resp offlineSystemPathResp
	if err := d.cl.request(ctx, http.MethodPost, "/file/system_path", nil, body, &resp); err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", offlineErrorFromCode(resp.Code, resp.Message)
	}
	fid := resp.dirFid()
	if fid == "" {
		return "", drive.NewOfflineError(drive.OfflineErrProvider, "", "quark: system_path returned no fid")
	}
	return fid, nil
}

// offlineSession remembers an accelerated session for a provider task.
func (d *Driver) rememberOfflineSession(taskID, session string) {
	if taskID == "" || session == "" {
		return
	}
	d.offlineMu.Lock()
	defer d.offlineMu.Unlock()
	if d.offlineSessions == nil {
		d.offlineSessions = map[string]string{}
	}
	d.offlineSessions[taskID] = session
}

func (d *Driver) offlineSession(taskID string) (string, bool) {
	d.offlineMu.Lock()
	defer d.offlineMu.Unlock()
	session, ok := d.offlineSessions[taskID]
	return session, ok
}

func (d *Driver) forgetOfflineSession(taskID string) {
	d.offlineMu.Lock()
	defer d.offlineMu.Unlock()
	delete(d.offlineSessions, taskID)
}

// quarkOfflineState maps Quark's task status onto the canonical offline state.
func quarkOfflineState(status int) drive.OfflineState {
	switch status {
	case 0:
		return drive.OfflineStateQueued
	case 1:
		return drive.OfflineStateRunning
	case 2:
		return drive.OfflineStateSucceeded
	case 3:
		return drive.OfflineStateFailed
	case 4:
		return drive.OfflineStateCanceled
	default:
		return drive.OfflineStateRunning
	}
}

// selectQuarkFiles maps a provider-neutral selection onto Quark file_no values.
// The zero value selects every parsed file.
func selectQuarkFiles(files []offlineParseFile, selection drive.OfflineSelection) []int {
	if len(files) == 0 {
		return nil
	}
	selected := make([]bool, len(files))
	for i := range selected {
		selected[i] = true
	}
	switch selection.Mode {
	case drive.OfflineSelectionLargest:
		order := make([]int, len(files))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			return jsonInt64(files[order[a]].Size) > jsonInt64(files[order[b]].Size)
		})
		for i := range selected {
			selected[i] = false
		}
		for i := 0; i < selection.Largest && i < len(order); i++ {
			selected[order[i]] = true
		}
	case drive.OfflineSelectionInclude:
		for i, file := range files {
			selected[i] = quarkMatches(quarkFileName(file), selection.Include, selection.Exclude)
		}
	}
	out := make([]int, 0, len(files))
	for i, file := range files {
		if selected[i] {
			out = append(out, file.FileNo)
		}
	}
	return out
}

func quarkMatches(name string, include, exclude []string) bool {
	if len(include) > 0 {
		matched := false
		for _, pattern := range include {
			if quarkGlobMatch(pattern, name) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, pattern := range exclude {
		if quarkGlobMatch(pattern, name) {
			return false
		}
	}
	return true
}

func quarkGlobMatch(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	if ok, err := path.Match(pattern, name); err == nil && ok {
		return true
	}
	if strings.Contains(pattern, "/") {
		return false
	}
	ok, err := path.Match(pattern, path.Base(name))
	return err == nil && ok
}

func quarkFileName(file offlineParseFile) string {
	if file.FileName != "" {
		return file.FileName
	}
	if file.Path != "" {
		return path.Base(file.Path)
	}
	return file.TorrentName
}

func jsonInt64(value json.Number) int64 {
	parsed, err := value.Int64()
	if err != nil {
		return 0
	}
	return parsed
}

// isOfflineTaskMissing reports whether a provider lookup failed because the
// task no longer exists. drive.HTTPError classifies HTTP 404 as ErrNotFound.
func isOfflineTaskMissing(err error) bool {
	return errors.Is(err, drive.ErrNotFound)
}
