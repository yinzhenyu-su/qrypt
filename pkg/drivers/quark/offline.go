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
	// download_task_finish=1 means the resource is already cached, so the
	// staged results are complete from the start and a fetch may transfer
	// without waiting for the task to terminalize.
	available := parsed.ResourceInfo.DownloadTaskFinish == 1
	// The submit response's dir_fid is the directory that actually contains
	// the produced root; persist and return that same identity so a fresh
	// driver resolves the same parent after recovery.
	parentID := result.DirFid
	if parentID == "" {
		parentID = dirFid
	}
	d.rememberOfflineLayout(result.TaskID, result.Fid, parentID, available)
	observation := drive.OfflineDownload{
		ID:          result.TaskID,
		State:       drive.OfflineStateQueued,
		Destination: parentID,
		RemoteID:    result.Fid,
		Available:   available,
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
	// A succeeded task, or one the provider reported as already cached, has a
	// complete staged result set: resolve each file's relative path and pick up
	// any file the result list omitted.
	// A terminal failure or cancel is authoritative: return it before any
	// layout work so a listing error cannot mask the provider's state.
	if observation.State == drive.OfflineStateFailed || observation.State == drive.OfflineStateCanceled {
		return observation, nil
	}
	layout := d.offlineLayout(id)
	if observation.State == drive.OfflineStateSucceeded || layout.Available {
		files, err := d.resolveOfflineLayout(ctx, layout, observation.Files)
		if err != nil {
			if ctx.Err() != nil {
				return drive.OfflineDownload{}, ctx.Err()
			}
			// Keep a classified provider failure (auth, quota, path, ...)
			// intact; only an unclassified walk failure is transient.
			var classified *drive.OfflineError
			if errors.As(err, &classified) {
				return drive.OfflineDownload{}, err
			}
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				return drive.OfflineDownload{}, offlineErrorFromCode(apiErr.Code, apiErr.Message)
			}
			return drive.OfflineDownload{}, drive.NewOfflineError(
				drive.OfflineErrProviderUnavailable, "",
				fmt.Sprintf("quark: resolve staged layout: %v", err))
		}
		observation.Available = true
		observation.Files = files
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

// rememberOfflineLayout remembers, for a provider task, the entry the task
// produced, its staging parent, and whether its staged results are complete.
func (d *Driver) rememberOfflineLayout(taskID, rootID, parentID string, available bool) {
	if taskID == "" {
		return
	}
	d.offlineMu.Lock()
	defer d.offlineMu.Unlock()
	if d.offlineLayouts == nil {
		d.offlineLayouts = map[string]drive.OfflineLayout{}
	}
	// This is an explicit snapshot for one task generation, not a merge.
	// In particular a restore after an in-place retry may clear availability
	// that a previous generation reported.
	d.offlineLayouts[taskID] = drive.OfflineLayout{
		TaskID:    taskID,
		RootID:    rootID,
		ParentID:  parentID,
		Available: available,
	}
}

func (d *Driver) offlineLayout(taskID string) drive.OfflineLayout {
	d.offlineMu.Lock()
	defer d.offlineMu.Unlock()
	return d.offlineLayouts[taskID]
}

// RestoreOfflineLayout reseeds this driver from persisted task metadata so a
// recovered fetch resolves the task's original output tree instead of starting
// from an empty, process-local map.
func (d *Driver) RestoreOfflineLayout(layout drive.OfflineLayout) {
	d.rememberOfflineLayout(layout.TaskID, layout.RootID, layout.ParentID, layout.Available)
}

func (d *Driver) forgetOfflineSession(taskID string) {
	d.offlineMu.Lock()
	defer d.offlineMu.Unlock()
	delete(d.offlineSessions, taskID)
	delete(d.offlineLayouts, taskID)
}

// offlineTreeEntry is one staged entry discovered under the task's output root.
type offlineTreeEntry struct {
	fid   string
	name  string
	path  string
	size  int64
	isDir bool
}

// offlineMaxLayoutDepth bounds the provider tree walk. Exhausting it is an
// error: a truncated tree must never be published as a complete result set.
const offlineMaxLayoutDepth = 32

// resolveOfflineLayout fills each result file's staged relative path from the
// task's output root and appends files the result list omitted. Paths are
// relative to the staging destination, so a resource folder keeps its own name.
// It is only meaningful once the provider reports the results are complete.
func (d *Driver) resolveOfflineLayout(ctx context.Context, layout drive.OfflineLayout, files []drive.OfflineFile) ([]drive.OfflineFile, error) {
	if layout.RootID == "" {
		return files, nil
	}
	tree := map[string]offlineTreeEntry{}
	visited := map[string]bool{}
	if layout.ParentID != "" {
		entries, err := d.List(ctx, layout.ParentID)
		if err != nil {
			return nil, err
		}
		var root *drive.Entry
		for index := range entries {
			if entries[index].ID == layout.RootID {
				root = &entries[index]
				break
			}
		}
		switch {
		case root == nil:
			// A known root must be a child of the known staging parent. A
			// listing that does not show it is lagging or incomplete; walking
			// the root without its parent-derived prefix would silently drop
			// the resource folder, so treat the view as unusable and retry.
			return nil, fmt.Errorf("staged root %s is not listed under parent %s", layout.RootID, layout.ParentID)
		case root.IsDir:
			if err := d.collectOfflineTree(ctx, root.ID, root.Name, tree, visited, 0); err != nil {
				return nil, err
			}
		default:
			// A single-file source's root is the file itself, not a directory.
			tree[root.ID] = offlineTreeEntry{
				fid: root.ID, name: root.Name, path: root.Name, size: root.Size,
			}
		}
	} else if err := d.collectOfflineTree(ctx, layout.RootID, "", tree, visited, 0); err != nil {
		return nil, err
	}
	if len(tree) == 0 {
		return files, nil
	}
	seen := make(map[string]bool, len(files))
	out := make([]drive.OfflineFile, 0, len(files)+len(tree))
	for _, file := range files {
		if entry, ok := tree[file.RemoteID]; ok {
			file.Path = entry.path
			if file.Name == "" {
				file.Name = entry.name
			}
			if file.Size == 0 {
				file.Size = entry.size
			}
		}
		if file.RemoteID != "" {
			seen[file.RemoteID] = true
		}
		out = append(out, file)
	}
	// The provider's result list can omit staged files (for example ancillary
	// entries beside the main content). They are still inside the task's own
	// output root, so include them rather than mirroring a truncated tree.
	extra := make([]offlineTreeEntry, 0)
	for fid, entry := range tree {
		if seen[fid] || entry.isDir {
			continue
		}
		extra = append(extra, entry)
	}
	sort.Slice(extra, func(a, b int) bool { return extra[a].path < extra[b].path })
	for _, entry := range extra {
		out = append(out, drive.OfflineFile{
			Index:    len(out),
			Name:     entry.name,
			Size:     entry.size,
			RemoteID: entry.fid,
			Path:     entry.path,
		})
	}
	return out, nil
}

// collectOfflineTree walks the staged tree below fid, recording every entry's
// path relative to the output root. A listing failure, a cycle, or a tree
// deeper than offlineMaxLayoutDepth is returned rather than swallowed.
func (d *Driver) collectOfflineTree(ctx context.Context, fid, prefix string, out map[string]offlineTreeEntry, visited map[string]bool, depth int) error {
	if fid == "" {
		return nil
	}
	if depth > offlineMaxLayoutDepth {
		return fmt.Errorf("staged tree below %s exceeds %d levels", fid, offlineMaxLayoutDepth)
	}
	if visited[fid] {
		return fmt.Errorf("staged tree has a cycle at %s", fid)
	}
	visited[fid] = true
	entries, err := d.List(ctx, fid)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.ID == "" {
			continue
		}
		rel := entry.Name
		if prefix != "" {
			rel = prefix + "/" + entry.Name
		}
		out[entry.ID] = offlineTreeEntry{fid: entry.ID, name: entry.Name, path: rel, size: entry.Size, isDir: entry.IsDir}
		if entry.IsDir {
			if err := d.collectOfflineTree(ctx, entry.ID, rel, out, visited, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
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
