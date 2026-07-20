package quark

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/retry"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type Driver struct {
	drive.UnsupportedOperations
	cl                 *client
	urlCache           sync.Map
	cookie             string
	rootPath           string
	rootID             string
	limiter            *drive.BandwidthLimiter
	stateStore         drive.StateStore
	cookieSource       string
	cookieUpdated      time.Time
	debugMu            sync.Mutex
	debugUploads       map[string]quarkUploadDebug
	lastError          string
	instantUploadCount int64
}

type cookieState struct {
	Cookie    string    `json:"cookie,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type quarkUploadDebug struct {
	Name           string    `json:"name"`
	ParentID       string    `json:"parent_id"`
	TaskID         string    `json:"task_id"`
	UploadID       string    `json:"upload_id"`
	ObjKey         string    `json:"obj_key,omitempty"`
	PartSize       int64     `json:"part_size"`
	PartsSubmitted int       `json:"parts_submitted"`
	PartsCompleted int       `json:"parts_completed"`
	BytesTotal     int64     `json:"bytes_total"`
	BytesRead      int64     `json:"bytes_read"`
	Stage          string    `json:"stage"`
	LastError      string    `json:"last_error,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type uploadPartJob struct {
	number int
	data   []byte
}

type uploadPartResult struct {
	number int
	etag   string
	err    error
}

type uploadSessionState struct {
	Version  int                           `json:"version"`
	Sessions map[string]quarkUploadSession `json:"sessions,omitempty"`
}

type quarkUploadSession struct {
	Key       string          `json:"key"`
	ParentID  string          `json:"parent_id"`
	Name      string          `json:"name"`
	Size      int64           `json:"size"`
	MD5       string          `json:"md5"`
	SHA1      string          `json:"sha1"`
	TaskID    string          `json:"task_id"`
	UploadID  string          `json:"upload_id"`
	ObjKey    string          `json:"obj_key"`
	UploadURL string          `json:"upload_url"`
	Fid       string          `json:"fid"`
	Bucket    string          `json:"bucket"`
	Callback  json.RawMessage `json:"callback,omitempty"`
	AuthInfo  string          `json:"auth_info"`
	PartSize  int             `json:"part_size"`
	Etags     map[int]string  `json:"etags,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

const quarkUploadSessionStateFile = "quark_upload_sessions.json"
const quarkUploadSessionMaxAge = 24 * time.Hour
const quarkUploadSessionMaxEntries = 1024

func init() {
	drive.Register("quark", func(params drive.Params) (drive.Driver, error) {
		cookie := params["cookie"]
		if cookie == "" {
			return nil, fmt.Errorf("quark: missing cookie")
		}
		return New(cookie, Options{
			RootPath: params["root_path"],
			BaseURL:  params["base_url"],
			V2URL:    params["v2_url"],
		}), nil
	},
		drive.ParamDef{
			Name:        "cookie",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "Quark cloud drive authentication cookie",
			Example:     "k1=v1; k2=v2",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Virtual root path on the drive",
			Default:     "/",
			Example:     "/qrypt",
		},
		drive.ParamDef{
			Name:        "base_url",
			Type:        "string",
			Description: "Custom API base URL",
			Example:     "https://drive.quark.cn",
		},
		drive.ParamDef{
			Name:        "v2_url",
			Type:        "string",
			Description: "Custom API v2 URL",
			Example:     "https://drive-m.quark.cn",
		},
	)
}

type Options struct {
	RootPath string
	RootID   string
	BaseURL  string
	V2URL    string
}

func New(cookie string, opts Options) *Driver {
	rootID := opts.RootID
	if rootID == "" {
		rootID = "0"
	}
	d := &Driver{
		cl:           newClient(cookie, clientOptions{BaseURL: opts.BaseURL, V2URL: opts.V2URL}),
		cookie:       cookie,
		rootPath:     opts.RootPath,
		rootID:       rootID,
		cookieSource: "config",
		debugUploads: map[string]quarkUploadDebug{},
	}
	d.cl.onCookieUpdate = d.saveUpdatedCookie
	return d
}

func (d *Driver) Init(ctx context.Context) error {
	d.loadCookieState()
	if d.cookie == "" {
		return fmt.Errorf("quark: cookie is required")
	}
	var resp sortResp
	if err := d.cl.request(ctx, http.MethodGet, "/file/sort", map[string]string{
		"pdir_fid": d.rootID,
		"_size":    "1",
	}, nil, &resp); err != nil {
		return fmt.Errorf("quark: validate cookie: %w", err)
	}
	if err := apiError(resp.respEnvelope); err != nil {
		return err
	}
	if d.rootPath != "" && d.rootPath != "/" {
		rootID, err := d.resolvePathFrom(ctx, "0", d.rootPath)
		if err != nil {
			return fmt.Errorf("quark: resolve root_path %q: %w", d.rootPath, err)
		}
		d.rootID = rootID
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error {
	return nil
}

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
	d.pruneStoredUploadSessions()
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	parentID = d.resolve(parentID)
	pageSize := 100
	var firstResp sortResp
	err := d.cl.request(ctx, http.MethodGet, "/file/sort", map[string]string{
		"pdir_fid":             parentID,
		"_size":                strconv.Itoa(pageSize),
		"_page":                "1",
		"_fetch_total":         "1",
		"fetch_all_file":       "1",
		"fetch_risk_file_name": "1",
	}, nil, &firstResp)
	if err != nil {
		return nil, fmt.Errorf("quark: list: %w", err)
	}
	if err := apiError(firstResp.respEnvelope); err != nil {
		return nil, err
	}

	total := firstResp.Metadata.Total
	if total < len(firstResp.Data.List) {
		total = len(firstResp.Data.List)
	}
	allFiles := make([]file, total)
	copy(allFiles, firstResp.Data.List)

	if total > pageSize {
		totalPages := (total + pageSize - 1) / pageSize
		var wg sync.WaitGroup
		var once sync.Once
		var lastErr error
		for page := 2; page <= totalPages; page++ {
			wg.Add(1)
			go func(page int) {
				defer wg.Done()
				var resp sortResp
				err := d.cl.request(ctx, http.MethodGet, "/file/sort", map[string]string{
					"pdir_fid":             parentID,
					"_size":                strconv.Itoa(pageSize),
					"_page":                strconv.Itoa(page),
					"fetch_all_file":       "1",
					"fetch_risk_file_name": "1",
				}, nil, &resp)
				if err != nil {
					once.Do(func() { lastErr = err })
					return
				}
				if err := apiError(resp.respEnvelope); err != nil {
					once.Do(func() { lastErr = err })
					return
				}
				offset := (page - 1) * pageSize
				if offset < len(allFiles) {
					copy(allFiles[offset:], resp.Data.List)
				}
			}(page)
		}
		wg.Wait()
		if lastErr != nil {
			return nil, lastErr
		}
	}

	entries := make([]drive.Entry, 0, len(allFiles))
	for _, item := range allFiles {
		if item.Fid == "" {
			continue
		}
		entries = append(entries, item.entry(parentID))
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	start := time.Now()
	downloadURL, err := d.downloadURL(ctx, entry.ID)
	if err != nil {
		logging.L.DebugfEvery("quark.read_url.error", time.Second, "[QUARK] ReadURL fid=%q offset=%d size=%d err=%v dur=%s", entry.ID, offset, size, err, time.Since(start))
		return nil, err
	}
	logging.L.DebugfEvery("quark.read_url", time.Second, "[QUARK] ReadURL fid=%q offset=%d size=%d dur=%s", entry.ID, offset, size, time.Since(start))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("quark: read: create request: %w", err)
	}
	if size > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))
	}
	httpStart := time.Now()
	resp, err := d.cl.doDownload(req)
	d.cl.recordMetric(ctx, drive.MetricEvent{
		Operation: "download",
		Method:    req.Method,
		URL:       util.URL(req.URL),
		Status:    responseStatus(resp),
		Duration:  time.Since(httpStart).String(),
		Request:   map[string]any{"range": req.Header.Get("Range")},
		Error:     errorString(err),
	})
	if err != nil {
		d.invalidateURL(entry.ID)
		logging.L.DebugfEvery("quark.read_http.error", time.Second, "[QUARK] ReadHTTP fid=%q offset=%d size=%d err=%v dur=%s", entry.ID, offset, size, err, time.Since(httpStart))
		return nil, fmt.Errorf("quark: read: download: %w", err)
	}
	logging.L.DebugfEvery("quark.read_http", time.Second, "[QUARK] ReadHTTP fid=%q offset=%d size=%d status=%d dur=%s", entry.ID, offset, size, resp.StatusCode, time.Since(httpStart))
	if resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		d.invalidateURL(entry.ID)
		return d.Read(ctx, entry, offset, size)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("quark: read: unexpected status %d", resp.StatusCode)
	}
	body := d.limiter.LimitDownload(ctx, resp.Body)
	return &traceReadCloser{
		ReadCloser: body,
		fid:        entry.ID,
		offset:     offset,
		size:       size,
		start:      time.Now(),
	}, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	now := time.Now()
	parentID = d.resolve(parentID)
	logging.L.InfofEvery("quark.mkdir_start", time.Second, "[QUARK] mkdir start parent=%q name=%q", parentID, name)
	data := map[string]any{
		"pdir_fid":      parentID,
		"file_name":     name,
		"dir_path":      "",
		"dir_init_lock": false,
	}
	var resp createDirResp
	if err := d.cl.request(ctx, http.MethodPost, "/file", nil, data, &resp); err != nil {
		logging.L.Warnf("[QUARK] mkdir request failed parent=%q name=%q err=%v", parentID, name, err)
		return drive.Entry{}, fmt.Errorf("quark: mkdir: %w", err)
	}
	if err := apiError(resp.respEnvelope); err != nil {
		logging.L.Warnf("[QUARK] mkdir api error parent=%q name=%q err=%v", parentID, name, err)
		return drive.Entry{}, err
	}
	logging.L.InfofEvery("quark.mkdir_complete", time.Second, "[QUARK] mkdir complete parent=%q name=%q id=%q", parentID, name, resp.Data.Fid)
	return drive.Entry{ID: resp.Data.Fid, ParentID: parentID, Name: name, IsDir: true, ModTime: now}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	data := map[string]any{
		"filelist":     []string{entry.ID},
		"to_pdir_fid":  d.resolve(dstParentID),
		"action_type":  1,
		"exclude_fids": []string{},
	}
	var resp respEnvelope
	if err := d.cl.request(ctx, http.MethodPost, "/file/move", nil, data, &resp); err != nil {
		return fmt.Errorf("quark: move: %w", err)
	}
	return apiError(resp)
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	data := map[string]any{
		"fid":       entry.ID,
		"file_name": newName,
	}
	var resp respEnvelope
	if err := d.cl.request(ctx, http.MethodPost, "/file/rename", nil, data, &resp); err != nil {
		return fmt.Errorf("quark: rename: %w", err)
	}
	return apiError(resp)
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	data := map[string]any{
		"action_type":  1,
		"exclude_fids": []string{},
		"filelist":     []string{entry.ID},
	}
	var resp respEnvelope
	if err := d.cl.request(ctx, http.MethodPost, "/file/delete", nil, data, &resp); err != nil {
		return fmt.Errorf("quark: delete: %w", err)
	}
	return apiError(resp)
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	size := source.Size()
	parentID = d.resolve(parentID)
	putStart := time.Now()
	mtime := time.Now()
	logging.L.InfofEvery("quark.upload_start", time.Second, "[QUARK] upload start parent=%q name=%q size=%d", parentID, name, size)

	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	hashData, hasSourceHashes, err := quarkSourceHashes(source)
	if err != nil {
		return drive.Entry{}, err
	}
	sessionKey := ""
	var session quarkUploadSession
	var resumedSession bool
	if hasSourceHashes {
		sessionKey = d.uploadSessionKey(parentID, name, size, hashData)
		session, resumedSession = d.loadUploadSession(sessionKey)
	}

	var preResp upPreResp
	if resumedSession {
		preResp = session.preResp()
		logging.L.InfofEvery("quark.upload_resume", time.Second, "[QUARK] upload resume name=%q task=%q upload_id=%q completed_parts=%d", name, session.TaskID, session.UploadID, len(session.Etags))
	} else {
		preData := map[string]any{
			"ccp_hash_update": true,
			"file_name":       name,
			"l_created_at":    mtime.UnixMilli(),
			"l_updated_at":    mtime.UnixMilli(),
			"pdir_fid":        parentID,
			"size":            size,
			"format_type":     0,
		}
		if err := d.cl.request(ctx, http.MethodPost, "/file/upload/pre", nil, preData, &preResp); err != nil {
			logging.L.Warnf("[QUARK] upload pre failed parent=%q name=%q size=%d err=%v", parentID, name, size, err)
			return drive.Entry{}, fmt.Errorf("quark: upload pre: %w", err)
		}
		if err := apiError(preResp.respEnvelope); err != nil {
			logging.L.Warnf("[QUARK] upload pre api error parent=%q name=%q size=%d err=%v", parentID, name, size, err)
			return drive.Entry{}, err
		}
	}
	logging.L.InfofEvery("quark.upload_pre_ok", time.Second, "[QUARK] upload pre ok name=%q task=%q upload_id=%q part_size=%d finish=%t", name, preResp.Data.TaskID, preResp.Data.UploadID, preResp.Metadata.PartSize, preResp.Data.Finish)
	d.setUploadDebug(preResp.Data.TaskID, quarkUploadDebug{
		Name:       name,
		ParentID:   parentID,
		TaskID:     preResp.Data.TaskID,
		UploadID:   preResp.Data.UploadID,
		ObjKey:     preResp.Data.ObjKey,
		PartSize:   int64(preResp.Metadata.PartSize),
		BytesTotal: size,
		Stage:      "pre_ok",
		StartedAt:  putStart,
		UpdatedAt:  time.Now(),
	})
	defer d.finishUploadDebug(preResp.Data.TaskID)
	if preResp.Data.Finish && preResp.Data.Fid != "" {
		drive.ReportUploadPhase(req.Progress, drive.UploadPhaseInstant)
		d.updateUploadDebug(preResp.Data.TaskID, func(item *quarkUploadDebug) {
			item.Stage = "instant_finish"
			item.BytesRead = size
		})
		finalFid, err := d.uploadFinish(ctx, preResp.Data.Fid, preResp.Data.ObjKey, preResp.Data.TaskID)
		if err != nil {
			d.setUploadDebugError(preResp.Data.TaskID, err)
			logging.L.Warnf("[QUARK] instant upload finish failed name=%q task=%q fid=%q err=%v", name, preResp.Data.TaskID, preResp.Data.Fid, err)
			return drive.Entry{}, fmt.Errorf("quark: upload finish: %w", err)
		}
		logging.L.InfofEvery("quark.instant_upload_complete", time.Second, "[QUARK] instant upload complete name=%q fid=%q size=%d dur=%s", name, finalFid, size, time.Since(putStart))
		d.debugMu.Lock()
		d.instantUploadCount++
		d.debugMu.Unlock()
		d.deleteUploadSession(sessionKey)
		return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: size, ModTime: mtime}, nil
	}

	partSize := preResp.Metadata.PartSize
	if partSize <= 0 {
		partSize = 4 * 1024 * 1024
	}

	if hasSourceHashes && !resumedSession {
		finished, finalFid, err := d.updateUploadHash(ctx, preResp.Data.TaskID, preResp.Data.Fid, preResp.Data.ObjKey, hashData, name, size, putStart)
		if err != nil {
			return drive.Entry{}, err
		}
		if finished {
			drive.ReportUploadPhase(req.Progress, drive.UploadPhaseInstant)
			d.deleteUploadSession(sessionKey)
			return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: size, ModTime: mtime}, nil
		}
		session = uploadSessionFromPre(sessionKey, parentID, name, size, hashData, preResp, partSize)
	} else if resumedSession && session.Etags == nil {
		session.Etags = map[int]string{}
	}

	md5Hash := md5.New()
	sha1Hash := sha1.New()
	sourceFile, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("quark: upload source open: %w", err)
	}
	defer sourceFile.Close()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
	reader := io.Reader(sourceFile)
	if !hasSourceHashes {
		reader = io.TeeReader(sourceFile, io.MultiWriter(md5Hash, sha1Hash))
	}

	buf := make([]byte, partSize)
	etagsByPart := map[int]string{}
	if resumedSession {
		for part, etag := range session.Etags {
			etagsByPart[part] = etag
		}
	}
	var totalRead int64
	var totalParts int
	var submittedParts int
	var completedParts int
	partNumber := 1
	jobs := make(chan uploadPartJob, partUploadWorkers)
	results := make(chan uploadPartResult, partUploadWorkers)
	done := make(chan struct{})
	jobsClosed := false
	closeJobs := func() {
		if !jobsClosed {
			close(jobs)
			jobsClosed = true
		}
	}
	defer close(done)
	defer closeJobs()

	var uploadWG sync.WaitGroup
	for i := 0; i < partUploadWorkers; i++ {
		uploadWG.Add(1)
		go func() {
			defer uploadWG.Done()
			for {
				select {
				case job, ok := <-jobs:
					if !ok {
						return
					}
					logging.L.Debugf("[QUARK] upload part start name=%q task=%q part=%d bytes=%d", name, preResp.Data.TaskID, job.number, len(job.data))
					etag, err := d.uploadPart(ctx, &preResp, job.number, job.data)
					if err != nil {
						logging.L.Warnf("[QUARK] upload part failed name=%q task=%q part=%d bytes=%d err=%v", name, preResp.Data.TaskID, job.number, len(job.data), err)
					} else {
						drive.ReportUploadProgress(req.Progress, int64(len(job.data)))
						logging.L.Debugf("[QUARK] upload part complete name=%q task=%q part=%d etag=%q", name, preResp.Data.TaskID, job.number, etag)
					}
					select {
					case results <- uploadPartResult{number: job.number, etag: etag, err: err}:
					case <-done:
						return
					}
				case <-done:
					return
				}
			}
		}()
	}
	handleResult := func(result uploadPartResult) error {
		if result.err != nil {
			return fmt.Errorf("quark: upload part %d: %w", result.number, result.err)
		}
		etagsByPart[result.number] = result.etag
		if sessionKey != "" {
			if session.Etags == nil {
				session.Etags = map[int]string{}
			}
			session.Etags[result.number] = result.etag
			d.saveUploadSession(session)
		}
		return nil
	}
	receiveResult := func() error {
		result := <-results
		completedParts++
		return handleResult(result)
	}
	sendJob := func(job uploadPartJob) error {
		for {
			select {
			case jobs <- job:
				submittedParts++
				return nil
			case result := <-results:
				completedParts++
				if err := handleResult(result); err != nil {
					return err
				}
			}
		}
	}

	for {
		n, readErr := io.ReadFull(reader, buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if _, ok := etagsByPart[partNumber]; ok {
				drive.ReportUploadProgress(req.Progress, int64(len(data)))
			} else if err := sendJob(uploadPartJob{number: partNumber, data: data}); err != nil {
				d.setUploadDebugError(preResp.Data.TaskID, err)
				return drive.Entry{}, d.resumedUploadSessionError(resumedSession, sessionKey, err)
			}
			totalRead += int64(n)
			d.updateUploadDebug(preResp.Data.TaskID, func(item *quarkUploadDebug) {
				item.Stage = "uploading_parts"
				item.BytesRead = totalRead
				item.PartsSubmitted = submittedParts
			})
			partNumber++
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			logging.L.Warnf("[QUARK] upload read body failed name=%q task=%q total_read=%d err=%v", name, preResp.Data.TaskID, totalRead, readErr)
			d.setUploadDebugError(preResp.Data.TaskID, readErr)
			return drive.Entry{}, fmt.Errorf("quark: upload: read body: %w", readErr)
		}
	}
	totalParts = partNumber - 1
	closeJobs()
	for completedParts < submittedParts {
		if err := receiveResult(); err != nil {
			d.setUploadDebugError(preResp.Data.TaskID, err)
			return drive.Entry{}, d.resumedUploadSessionError(resumedSession, sessionKey, err)
		}
		d.updateUploadDebug(preResp.Data.TaskID, func(item *quarkUploadDebug) {
			item.PartsCompleted = completedParts
		})
	}
	uploadWG.Wait()

	etags := make([]string, 0, totalParts)
	for i := 1; i <= totalParts; i++ {
		etags = append(etags, etagsByPart[i])
	}
	if totalRead == 0 {
		logging.L.Debugf("[QUARK] upload empty part start name=%q task=%q", name, preResp.Data.TaskID)
		etag, err := d.uploadPart(ctx, &preResp, 1, []byte{})
		if err != nil {
			d.setUploadDebugError(preResp.Data.TaskID, err)
			logging.L.Warnf("[QUARK] upload empty part failed name=%q task=%q err=%v", name, preResp.Data.TaskID, err)
			return drive.Entry{}, d.resumedUploadSessionError(resumedSession, sessionKey, fmt.Errorf("quark: upload part 1: %w", err))
		}
		etags = append(etags, etag)
		if sessionKey != "" {
			if session.Etags == nil {
				session.Etags = map[int]string{}
			}
			session.Etags[1] = etag
			d.saveUploadSession(session)
		}
	}

	if !hasSourceHashes {
		hashData = map[string]any{
			"md5":  fmt.Sprintf("%X", md5Hash.Sum(nil)),
			"sha1": fmt.Sprintf("%X", sha1Hash.Sum(nil)),
		}
		finished, finalFid, err := d.updateUploadHash(ctx, preResp.Data.TaskID, preResp.Data.Fid, preResp.Data.ObjKey, hashData, name, totalRead, putStart)
		if err != nil {
			return drive.Entry{}, err
		}
		if finished {
			drive.ReportUploadPhase(req.Progress, drive.UploadPhaseInstant)
			return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: totalRead, ModTime: mtime}, nil
		}
	}
	d.updateUploadDebug(preResp.Data.TaskID, func(item *quarkUploadDebug) { item.Stage = "oss_complete" })
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	if err := d.ossComplete(ctx, &preResp, etags); err != nil {
		d.setUploadDebugError(preResp.Data.TaskID, err)
		logging.L.Warnf("[QUARK] upload complete multipart failed name=%q task=%q parts=%d err=%v", name, preResp.Data.TaskID, len(etags), err)
		return drive.Entry{}, d.resumedUploadSessionError(resumedSession, sessionKey, fmt.Errorf("quark: upload complete: %w", err))
	}
	d.updateUploadDebug(preResp.Data.TaskID, func(item *quarkUploadDebug) { item.Stage = "finish" })
	finalFid, err := d.uploadFinish(ctx, preResp.Data.Fid, preResp.Data.ObjKey, preResp.Data.TaskID)
	if err != nil {
		d.setUploadDebugError(preResp.Data.TaskID, err)
		logging.L.Warnf("[QUARK] upload finish failed name=%q task=%q fid=%q err=%v", name, preResp.Data.TaskID, preResp.Data.Fid, err)
		return drive.Entry{}, fmt.Errorf("quark: upload finish: %w", err)
	}
	logging.L.InfofEvery("quark.upload_complete", time.Second, "[QUARK] upload complete name=%q fid=%q size=%d parts=%d dur=%s", name, finalFid, totalRead, len(etags), time.Since(putStart))
	d.deleteUploadSession(sessionKey)
	return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: totalRead, ModTime: mtime}, nil
}

func quarkSourceHashes(source drive.ReadOnlyFileSource) (map[string]any, bool, error) {
	md5Sum, ok := drive.SourceHash(source, drive.HashMD5)
	if !ok {
		return nil, false, nil
	}
	sha1Sum, ok := drive.SourceHash(source, drive.HashSHA1)
	if !ok {
		return nil, false, nil
	}
	if len(md5Sum) != md5.Size {
		return nil, false, drive.NonRetryable(fmt.Errorf("quark: source MD5 metadata has %d bytes, want %d", len(md5Sum), md5.Size))
	}
	if len(sha1Sum) != sha1.Size {
		return nil, false, drive.NonRetryable(fmt.Errorf("quark: source SHA-1 metadata has %d bytes, want %d", len(sha1Sum), sha1.Size))
	}
	return map[string]any{
		"md5":  fmt.Sprintf("%X", md5Sum),
		"sha1": fmt.Sprintf("%X", sha1Sum),
	}, true, nil
}

func (d *Driver) updateUploadHash(ctx context.Context, taskID, fid, objKey string, hashData map[string]any, name string, size int64, startedAt time.Time) (bool, string, error) {
	hashData["task_id"] = taskID
	var hashResp hashResp
	d.updateUploadDebug(taskID, func(item *quarkUploadDebug) { item.Stage = "hash_update" })
	if err := d.cl.request(ctx, http.MethodPost, "/file/update/hash", nil, hashData, &hashResp); err != nil {
		d.setUploadDebugError(taskID, err)
		logging.L.Warnf("[QUARK] upload hash update failed name=%q task=%q size=%d err=%v", name, taskID, size, err)
		return false, "", fmt.Errorf("quark: upload hash: %w", err)
	}
	logging.L.InfofEvery("quark.upload_hash_ok", time.Second, "[QUARK] upload hash update ok name=%q task=%q size=%d finish=%t", name, taskID, size, hashResp.Data.Finish)
	if !hashResp.Data.Finish {
		return false, "", nil
	}
	if hashResp.Data.Fid != "" {
		fid = hashResp.Data.Fid
	}
	finalFid, err := d.uploadFinish(ctx, fid, objKey, taskID)
	if err != nil {
		d.setUploadDebugError(taskID, err)
		logging.L.Warnf("[QUARK] hash-finished upload finish failed name=%q task=%q fid=%q err=%v", name, taskID, fid, err)
		return false, "", fmt.Errorf("quark: upload finish: %w", err)
	}
	logging.L.InfofEvery("quark.hash_finished_upload_complete", time.Second, "[QUARK] hash-finished upload complete name=%q fid=%q size=%d dur=%s", name, finalFid, size, time.Since(startedAt))
	d.debugMu.Lock()
	d.instantUploadCount++
	d.debugMu.Unlock()
	return true, finalFid, nil
}

func (d *Driver) RequiredUploadHashes() []drive.HashAlgorithm {
	return []drive.HashAlgorithm{drive.HashMD5, drive.HashSHA1}
}

func (d *Driver) ResolvePath(ctx context.Context, path string) (string, error) {
	return d.resolvePathFrom(ctx, d.rootID, path)
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	activeUploads := d.activeUploadDebug()
	urlCacheCount := 0
	d.urlCache.Range(func(_, _ any) bool {
		urlCacheCount++
		return true
	})
	health := "ok"
	if d.getLastError() != "" {
		health = "degraded"
	}
	return drive.DebugSnapshot{
		Driver:      "quark",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			"active_uploads":        len(activeUploads),
			"url_cache_count":       urlCacheCount,
			drive.DebugStatRootID:   d.rootID,
			drive.DebugStatRootPath: d.rootPath,
		},
		Extra: map[string]any{
			drive.DebugExtraCredentialSource:   d.cookieSource,
			drive.DebugExtraCredentialUpdated:  d.cookieUpdated,
			drive.DebugExtraLastError:          d.getLastError(),
			drive.DebugExtraInstantUploadCount: d.instantUploadCount,
		},
	}, nil
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.cl.metricEvents(since), nil
}

func (d *Driver) setLastError(err error) {
	if err == nil {
		return
	}
	d.debugMu.Lock()
	d.lastError = err.Error()
	d.debugMu.Unlock()
}

func (d *Driver) getLastError() string {
	d.debugMu.Lock()
	defer d.debugMu.Unlock()
	return d.lastError
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	var resp struct {
		Total int64 `json:"total_capacity"`
		Used  int64 `json:"use_capacity"`
	}
	err := d.cl.request(ctx, http.MethodGet, "/member", map[string]string{
		"uc_param_str":    "",
		"fetch_subscribe": "true",
		"_ch":             "home",
		"fetch_identity":  "true",
	}, nil, &resp)
	if err != nil {
		return drive.Space{}, fmt.Errorf("quark: space: %w", err)
	}
	return drive.Space{
		Total: resp.Total,
		Free:  resp.Total - resp.Used,
	}, nil
}

func (d *Driver) loadCookieState() {
	if d.stateStore == nil {
		return
	}
	var state cookieState
	err := d.stateStore.LoadJSON("quark_cookie.json", &state)
	if err != nil {
		return
	}
	if state.Cookie != "" {
		d.cookie = state.Cookie
		d.cl.setCookie(state.Cookie)
		d.cookieSource = "state"
	}
	d.cookieUpdated = state.UpdatedAt
}

func (d *Driver) saveUpdatedCookie(cookie string) {
	if cookie == "" {
		return
	}
	d.cookie = cookie
	d.cookieSource = "response"
	d.cookieUpdated = time.Now()
	if d.stateStore == nil {
		return
	}
	if err := d.stateStore.SaveJSON("quark_cookie.json", cookieState{
		Cookie:    cookie,
		UpdatedAt: d.cookieUpdated,
	}); err != nil {
		logging.L.Warnf("[QUARK] save updated cookie state failed: %v", err)
	}
}

func (d *Driver) uploadSessionKey(parentID, name string, size int64, hashData map[string]any) string {
	md5Hex, _ := hashData["md5"].(string)
	sha1Hex, _ := hashData["sha1"].(string)
	return util.UploadSessionKey(parentID, name, size, md5Hex, sha1Hex)
}

func (d *Driver) loadUploadSession(key string) (quarkUploadSession, bool) {
	return d.uploadSessionStore().Load(key)
}

func (d *Driver) saveUploadSession(session quarkUploadSession) {
	d.uploadSessionStore().Save(session)
}

func (d *Driver) deleteUploadSession(key string) {
	d.uploadSessionStore().Delete(key)
}

func (d *Driver) pruneStoredUploadSessions() {
	d.uploadSessionStore().Prune()
}

func (d *Driver) prunedUploadSessions(state uploadSessionState, now time.Time) (uploadSessionState, bool) {
	state.Version = 1
	sessions, changed := d.uploadSessionStore().PrunedForTest(state.Sessions, now)
	state.Sessions = sessions
	return state, changed
}

func (d *Driver) uploadSessionStore() *util.UploadSessionStore[quarkUploadSession] {
	return util.NewUploadSessionStore(util.UploadSessionStoreOptions[quarkUploadSession]{
		Store:      d.stateStore,
		File:       quarkUploadSessionStateFile,
		MaxAge:     quarkUploadSessionMaxAge,
		MaxEntries: quarkUploadSessionMaxEntries,
		Key: func(session quarkUploadSession) string {
			return session.Key
		},
		Valid: func(key string, session quarkUploadSession) bool {
			return session.Key != "" && len(session.Etags) > 0
		},
		UpdatedAt: func(session quarkUploadSession) time.Time {
			return session.UpdatedAt
		},
		Touch: func(session *quarkUploadSession, now time.Time) {
			session.UpdatedAt = now
		},
		OnError: func(err error) {
			logging.L.Warnf("[QUARK] upload session state failed err=%v", err)
		},
	})
}

func (d *Driver) resumedUploadSessionError(resumed bool, key string, err error) error {
	if resumed && (drive.IsNonRetryable(err) || invalidResumedUploadSession(err)) {
		d.deleteUploadSession(key)
		return fmt.Errorf("quark: resumed upload session invalid, will retry from scratch: %v", err)
	}
	return err
}

func uploadSessionFromPre(key, parentID, name string, size int64, hashData map[string]any, pre upPreResp, partSize int) quarkUploadSession {
	md5Hex, _ := hashData["md5"].(string)
	sha1Hex, _ := hashData["sha1"].(string)
	return quarkUploadSession{
		Key:       key,
		ParentID:  parentID,
		Name:      name,
		Size:      size,
		MD5:       md5Hex,
		SHA1:      sha1Hex,
		TaskID:    pre.Data.TaskID,
		UploadID:  pre.Data.UploadID,
		ObjKey:    pre.Data.ObjKey,
		UploadURL: pre.Data.UploadURL,
		Fid:       pre.Data.Fid,
		Bucket:    pre.Data.Bucket,
		Callback:  append(json.RawMessage(nil), pre.Data.Callback...),
		AuthInfo:  pre.Data.AuthInfo,
		PartSize:  partSize,
		Etags:     map[int]string{},
	}
}

func (s quarkUploadSession) preResp() upPreResp {
	var pre upPreResp
	pre.Data.TaskID = s.TaskID
	pre.Data.UploadID = s.UploadID
	pre.Data.ObjKey = s.ObjKey
	pre.Data.UploadURL = s.UploadURL
	pre.Data.Fid = s.Fid
	pre.Data.Bucket = s.Bucket
	pre.Data.Callback = append(json.RawMessage(nil), s.Callback...)
	pre.Data.AuthInfo = s.AuthInfo
	pre.Metadata.PartSize = s.PartSize
	return pre
}

func (d *Driver) setUploadDebug(taskID string, item quarkUploadDebug) {
	if taskID == "" {
		return
	}
	d.debugMu.Lock()
	d.debugUploads[taskID] = item
	d.debugMu.Unlock()
}

func (d *Driver) updateUploadDebug(taskID string, update func(*quarkUploadDebug)) {
	if taskID == "" {
		return
	}
	d.debugMu.Lock()
	item := d.debugUploads[taskID]
	update(&item)
	item.UpdatedAt = time.Now()
	d.debugUploads[taskID] = item
	d.debugMu.Unlock()
}

func (d *Driver) setUploadDebugError(taskID string, err error) {
	if err == nil {
		return
	}
	d.updateUploadDebug(taskID, func(item *quarkUploadDebug) {
		item.Stage = "error"
		item.LastError = err.Error()
	})
}

func (d *Driver) finishUploadDebug(taskID string) {
	if taskID == "" {
		return
	}
	d.debugMu.Lock()
	delete(d.debugUploads, taskID)
	d.debugMu.Unlock()
}

func (d *Driver) activeUploadDebug() []quarkUploadDebug {
	d.debugMu.Lock()
	defer d.debugMu.Unlock()
	uploads := make([]quarkUploadDebug, 0, len(d.debugUploads))
	for _, upload := range d.debugUploads {
		uploads = append(uploads, upload)
	}
	return uploads
}

func (d *Driver) resolvePathFrom(ctx context.Context, rootID, path string) (string, error) {
	currentID := d.resolve(rootID)
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if segment == "" {
			continue
		}
		entries, err := d.List(ctx, currentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.Name == segment {
				currentID = entry.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("quark: child not found: %s", segment)
		}
	}
	return currentID, nil
}

func (d *Driver) resolve(id string) string {
	if id == "" || id == "0" || id == "/" {
		return d.rootID
	}
	return id
}

func (d *Driver) getURL(fid string) (string, bool) {
	if value, ok := d.urlCache.Load(fid); ok {
		cached := value.(cachedURL)
		if time.Now().Before(cached.expiry) {
			return cached.url, true
		}
	}
	return "", false
}

func (d *Driver) setURL(fid, url string) {
	d.urlCache.Store(fid, cachedURL{url: url, expiry: time.Now().Add(10 * time.Minute)})
}

func (d *Driver) invalidateURL(fid string) {
	d.urlCache.Delete(fid)
}

func (d *Driver) downloadURL(ctx context.Context, fid string) (string, error) {
	if url, ok := d.getURL(fid); ok {
		return url, nil
	}
	var resp downResp
	if err := d.cl.request(ctx, http.MethodPost, "/file/download", nil, map[string]any{
		"fids": []string{fid},
	}, &resp); err != nil {
		return "", fmt.Errorf("quark: get download url: %w", err)
	}
	if err := apiError(resp.respEnvelope); err != nil {
		return "", err
	}
	if len(resp.Data) == 0 {
		return "", fmt.Errorf("quark: no download url found")
	}
	url := resp.Data[0].DownloadURL
	d.setURL(fid, url)
	return url, nil
}

func (d *Driver) uploadFinish(ctx context.Context, fid, objKey, taskID string) (string, error) {
	var resp struct {
		respEnvelope
		Data struct {
			Fid string `json:"fid"`
		} `json:"data"`
	}
	if err := d.cl.request(ctx, http.MethodPost, "/file/upload/finish", nil, map[string]any{
		"obj_key": objKey,
		"task_id": taskID,
	}, &resp); err != nil {
		return fid, err
	}
	if err := apiError(resp.respEnvelope); err != nil {
		return fid, err
	}
	if resp.Data.Fid != "" {
		return resp.Data.Fid, nil
	}
	return fid, nil
}

func (d *Driver) ossComplete(ctx context.Context, pre *upPreResp, etags []string) error {
	if len(etags) == 0 {
		return nil
	}
	var xmlBody strings.Builder
	xmlBody.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<CompleteMultipartUpload>`)
	for i, etag := range etags {
		xmlBody.WriteString(fmt.Sprintf(`
<Part>
<PartNumber>%d</PartNumber>
<ETag>%s</ETag>
</Part>`, i+1, etag))
	}
	xmlBody.WriteString(`
</CompleteMultipartUpload>`)
	body := xmlBody.String()
	bodyMD5 := md5.Sum([]byte(body))
	contentMd5 := base64.StdEncoding.EncodeToString(bodyMD5[:])

	for attempt := 0; attempt <= ossMaxRetries; attempt++ {
		timeStr := time.Now().UTC().Format(http.TimeFormat)
		callbackB64 := base64.StdEncoding.EncodeToString(pre.Data.Callback)
		ossPath := pre.Data.ObjKey
		if pre.Data.Bucket != "" {
			ossPath = pre.Data.Bucket + "/" + ossPath
		}
		authMeta := fmt.Sprintf("POST\n%s\napplication/xml\n%s\nx-oss-callback:%s\nx-oss-date:%s\nx-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit\n/%s?uploadId=%s",
			contentMd5, timeStr, callbackB64, timeStr, ossPath, pre.Data.UploadID)
		var authResp upAuthResp
		err := d.cl.request(ctx, http.MethodPost, "/file/upload/auth", nil, map[string]any{
			"auth_info": pre.Data.AuthInfo,
			"auth_meta": authMeta,
			"task_id":   pre.Data.TaskID,
		}, &authResp)
		if err != nil {
			if attempt < ossMaxRetries {
				logging.L.WarnfEvery("quark.oss_complete_auth_retry", time.Second, "[QUARK] oss complete auth failed; retry task=%q attempt=%d err=%v", pre.Data.TaskID, attempt+1, err)
				if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
					return err
				}
				continue
			}
			logging.L.Warnf("[QUARK] oss complete auth failed task=%q attempts=%d err=%v", pre.Data.TaskID, attempt+1, err)
			return err
		}

		reqCtx, cancel := context.WithTimeout(ctx, ossRequestTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, ossURL(pre)+"?uploadId="+pre.Data.UploadID, strings.NewReader(body))
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Authorization", authResp.Data.AuthKey)
		req.Header.Set("Content-MD5", contentMd5)
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("x-oss-callback", callbackB64)
		req.Header.Set("x-oss-date", timeStr)
		req.Header.Set("x-oss-user-agent", "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit")
		req.Header.Set("Referer", defaultReferer)
		req.Header.Set("User-Agent", defaultUserAgent)
		start := time.Now()
		resp, err := d.cl.ossClient.Do(req)
		cancel()
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "oss_complete",
			Method:    req.Method,
			URL:       util.URL(req.URL),
			Status:    responseStatus(resp),
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"headers": util.HeaderKeys(req.Header)},
			Error:     errorString(err),
		})
		if err != nil {
			if attempt < ossMaxRetries {
				logging.L.WarnfEvery("quark.oss_complete_http_retry", time.Second, "[QUARK] oss complete http failed; retry task=%q attempt=%d err=%v", pre.Data.TaskID, attempt+1, err)
				if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
					return err
				}
				continue
			}
			logging.L.Warnf("[QUARK] oss complete http failed task=%q attempts=%d err=%v", pre.Data.TaskID, attempt+1, err)
			return fmt.Errorf("oss complete: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if attempt < ossMaxRetries {
			logging.L.WarnfEvery("quark.oss_complete_status_retry", time.Second, "[QUARK] oss complete status retry task=%q attempt=%d status=%d", pre.Data.TaskID, attempt+1, resp.StatusCode)
			if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
				return err
			}
			continue
		}
		logging.L.Warnf("[QUARK] oss complete status failed task=%q attempts=%d status=%d", pre.Data.TaskID, attempt+1, resp.StatusCode)
		var statusErr error = uploadStatusError{op: "oss complete", status: resp.StatusCode}
		if nonRetryableUploadStatus(resp.StatusCode) {
			statusErr = drive.NonRetryable(statusErr)
		}
		return statusErr
	}
	return nil
}

func (d *Driver) uploadPart(ctx context.Context, pre *upPreResp, partNumber int, data []byte) (string, error) {
	logging.L.DebugfEvery("quark.upload_part.enter", time.Second, "[QUARK] upload part enter task=%q part=%d bytes=%d bucket=%q obj=%q upload_url=%q", pre.Data.TaskID, partNumber, len(data), pre.Data.Bucket, pre.Data.ObjKey, pre.Data.UploadURL)
	for attempt := 0; attempt <= ossMaxRetries; attempt++ {
		dateStr := time.Now().UTC().Format(http.TimeFormat)
		ossPath := pre.Data.ObjKey
		if pre.Data.Bucket != "" {
			ossPath = pre.Data.Bucket + "/" + ossPath
		}
		authMeta := fmt.Sprintf("PUT\n\napplication/octet-stream\n%s\nx-oss-date:%s\nx-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit\n/%s?partNumber=%d&uploadId=%s",
			dateStr, dateStr, ossPath, partNumber, pre.Data.UploadID)
		authStart := time.Now()
		var authResp upAuthResp
		err := d.cl.request(ctx, http.MethodPost, "/file/upload/auth", nil, map[string]any{
			"auth_info":   pre.Data.AuthInfo,
			"auth_meta":   authMeta,
			"task_id":     pre.Data.TaskID,
			"part_number": partNumber,
		}, &authResp)
		authDur := time.Since(authStart)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if attempt < ossMaxRetries {
				logging.L.WarnfEvery("quark.upload_part_auth_retry", time.Second, "[QUARK] upload part auth failed; retry task=%q part=%d attempt=%d err=%v", pre.Data.TaskID, partNumber, attempt+1, err)
				if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
					return "", err
				}
				continue
			}
			logging.L.Warnf("[QUARK] upload part auth failed task=%q part=%d attempts=%d err=%v", pre.Data.TaskID, partNumber, attempt+1, err)
			return "", err
		}

		logging.L.DebugfEvery("quark.upload_part.auth", time.Second, "[QUARK] upload part auth done task=%q part=%d auth=%s", pre.Data.TaskID, partNumber, authDur)
		ossURLStr := ossURL(pre) + "?partNumber=" + strconv.Itoa(partNumber) + "&uploadId=" + pre.Data.UploadID
		logging.L.DebugfEvery("quark.upload_part.oss_start", time.Second, "[QUARK] upload part oss put start task=%q part=%d url=%q", pre.Data.TaskID, partNumber, ossURLStr)
		ossStart := time.Now()
		body := d.limiter.LimitUpload(ctx, bytes.NewReader(data))
		reqCtx, cancel := context.WithTimeout(ctx, ossRequestTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPut, ossURLStr, body)
		if err != nil {
			cancel()
			return "", err
		}
		req.Header.Set("Authorization", authResp.Data.AuthKey)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("x-oss-date", dateStr)
		req.Header.Set("x-oss-user-agent", "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit")
		req.Header.Set("Referer", defaultReferer)
		resp, err := d.cl.ossClient.Do(req)
		cancel()
		ossDur := time.Since(ossStart)
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "oss_upload_part",
			Method:    req.Method,
			URL:       util.URL(req.URL),
			Status:    responseStatus(resp),
			Duration:  ossDur.String(),
			Request:   map[string]any{"part_number": partNumber, "bytes": len(data), "headers": util.HeaderKeys(req.Header)},
			Error:     errorString(err),
		})
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if attempt < ossMaxRetries {
				logging.L.WarnfEvery("quark.upload_part_http_retry", time.Second, "[QUARK] upload part http failed; retry task=%q part=%d attempt=%d err=%v", pre.Data.TaskID, partNumber, attempt+1, err)
				if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
					return "", err
				}
				continue
			}
			logging.L.Warnf("[QUARK] upload part http failed task=%q part=%d attempts=%d err=%v", pre.Data.TaskID, partNumber, attempt+1, err)
			return "", fmt.Errorf("upload part %d http: %w", partNumber, err)
		}
		etag := resp.Header.Get("Etag")
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			logging.L.DebugfEvery("quark.upload_part.done", time.Second, "[QUARK] upload part done task=%q part=%d bytes=%d auth=%s oss=%s", pre.Data.TaskID, partNumber, len(data), authDur, ossDur)
			return etag, nil
		}
		if attempt < ossMaxRetries {
			logging.L.WarnfEvery("quark.upload_part_status_retry", time.Second, "[QUARK] upload part status retry task=%q part=%d attempt=%d status=%d", pre.Data.TaskID, partNumber, attempt+1, resp.StatusCode)
			if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
				return "", err
			}
			continue
		}
		logging.L.Warnf("[QUARK] upload part status failed task=%q part=%d attempts=%d status=%d", pre.Data.TaskID, partNumber, attempt+1, resp.StatusCode)
		var statusErr error = uploadStatusError{op: fmt.Sprintf("upload part %d", partNumber), status: resp.StatusCode}
		if nonRetryableUploadStatus(resp.StatusCode) {
			statusErr = drive.NonRetryable(statusErr)
		}
		return "", statusErr
	}
	return "", nil
}

type uploadStatusError struct {
	op     string
	status int
}

func (e uploadStatusError) Error() string {
	return fmt.Sprintf("%s status %d", e.op, e.status)
}

func nonRetryableUploadStatus(status int) bool {
	return status >= 400 &&
		status < 500 &&
		status != http.StatusRequestTimeout &&
		status != http.StatusTooManyRequests &&
		status != http.StatusConflict
}

func invalidResumedUploadSession(err error) bool {
	var statusErr uploadStatusError
	return errors.As(err, &statusErr) && statusErr.status == http.StatusConflict
}

func ossURL(pre *upPreResp) string {
	host := pre.Data.UploadURL
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if idx := strings.Index(host, "/"); idx != -1 {
		host = host[:idx]
	}
	if pre.Data.Bucket != "" {
		return fmt.Sprintf("https://%s.%s/%s", pre.Data.Bucket, host, pre.Data.ObjKey)
	}
	return fmt.Sprintf("https://%s/%s", host, pre.Data.ObjKey)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func ossRetryDelay(attempt int) time.Duration {
	return retry.ExponentialBackoffWithOptions(attempt, ossRetryBaseDelay, 2*time.Minute, false)
}

func apiError(resp respEnvelope) error {
	if resp.Status < 400 && resp.Code == 0 {
		return nil
	}
	switch resp.Code {
	case 23001:
		return fmt.Errorf("quark: not found")
	case 23004:
		return nil
	case 23008:
		return fmt.Errorf("quark: directory already exists")
	}
	return fmt.Errorf("quark: api error: status=%d code=%d msg=%s", resp.Status, resp.Code, resp.Message)
}

type traceReadCloser struct {
	io.ReadCloser
	fid    string
	offset int64
	size   int64
	start  time.Time
	read   int64
}

func (r *traceReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.read += int64(n)
	return n, err
}

func (r *traceReadCloser) Close() error {
	err := r.ReadCloser.Close()
	logging.L.DebugfEvery("quark.read_body", time.Second, "[QUARK] ReadBody fid=%q offset=%d size=%d read=%d err=%v dur=%s", r.fid, r.offset, r.size, r.read, err, time.Since(r.start))
	return err
}

func responseStatus(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.BandwidthLimitInstaller = (*Driver)(nil)
