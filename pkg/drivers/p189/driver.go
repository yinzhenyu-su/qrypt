package p189

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const timeFormat = "2006-01-02 15:04:05"
const sliceMD5Size = 1 << 20
const uploadPartSize = 10 * 1024 * 1024

type batchTaskInfo struct {
	FileID   int64  `json:"fileId"`
	FileName string `json:"fileName"`
	IsFolder int    `json:"isFolder"`
}

type Driver struct {
	drive.UnsupportedOperations
	cl            *client
	rootID        int64
	rootPath      string
	limiter       *drive.BandwidthLimiter
	stateStore    drive.StateStore
	cookieSource  string
	cookieUpdated time.Time
}

type cookieState struct {
	Cookie                  string    `json:"cookie,omitempty"`
	UpdatedAt               time.Time `json:"updated_at,omitempty"`
	PasswordReloginFailedAt time.Time `json:"password_relogin_failed_at,omitempty"`
	PasswordReloginError    string    `json:"password_relogin_error,omitempty"`
}

type p189UploadSessionState struct {
	Version  int                          `json:"version"`
	Sessions map[string]p189UploadSession `json:"sessions,omitempty"`
}

type p189UploadSession struct {
	Key            string       `json:"key"`
	ParentID       string       `json:"parent_id"`
	Name           string       `json:"name"`
	Size           int64        `json:"size"`
	FileMD5        string       `json:"file_md5"`
	SliceMD5       string       `json:"slice_md5"`
	UploadFileID   string       `json:"upload_file_id"`
	PartSize       int64        `json:"part_size"`
	CompletedParts map[int]bool `json:"completed_parts,omitempty"`
	SavedAt        time.Time    `json:"saved_at"`
}

type p189UploadHashes struct {
	FileMD5  string
	SliceMD5 string
	Parts    []p189UploadPartMeta
}

type p189UploadPartMeta struct {
	Number    int
	Size      int64
	MD5Hex    string
	MD5Base64 string
}

const p189UploadSessionStateFile = "189_upload_sessions.json"
const p189UploadSessionMaxAge = 24 * time.Hour
const p189UploadSessionMaxEntries = 1024

func init() {
	drive.Register("189", func(params drive.Params) (drive.Driver, error) {
		cookie := params["cookie"]
		username := params["username"]
		password := params["password"]
		if cookie == "" && (username == "" || password == "") {
			return nil, fmt.Errorf("189: missing cookie, or username and password")
		}
		d := &Driver{
			cl:           newClient(cookie, username, password),
			rootPath:     params["root_path"],
			cookieSource: "config",
		}
		d.cl.onCookieUpdate = d.saveUpdatedCookie
		d.cl.onPasswordReloginState = d.savePasswordReloginState
		if rid := params["root_id"]; rid != "" {
			if id, err := strconv.ParseInt(rid, 10, 64); err == nil {
				d.rootID = id
			}
		}
		return d, nil
	},
		drive.ParamDef{
			Name:        "cookie",
			Type:        "string",
			Secret:      true,
			Description: "189 cloud drive authentication cookie (alternative to username/password)",
			Example:     "k1=v1; k2=v2",
		},
		drive.ParamDef{
			Name:        "username",
			Type:        "string",
			Description: "189 cloud drive account (phone number)",
			Example:     "18912345678",
		},
		drive.ParamDef{
			Name:        "password",
			Type:        "string",
			Secret:      true,
			Description: "189 cloud drive password",
			Example:     "your-password",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Virtual root path on the drive",
			Default:     "/",
			Example:     "/qrypt",
		},
		drive.ParamDef{
			Name:        "root_id",
			Type:        "string",
			Description: "Pre-resolved folder ID (skips root_path resolution)",
			Example:     "-11",
		},
	)
}

func (d *Driver) Init(ctx context.Context) error {
	d.loadCookieState()
	if err := d.cl.loginInit(ctx); err != nil {
		return fmt.Errorf("189: login init: %w", err)
	}
	if d.cl.username != "" {
		// SessionKey is required by upload APIs, but OpenList-compatible
		// read/list flows do not require it. Treat it as best-effort during
		// Init so read-only auth/list checks can still validate credentials.
		_ = d.cl.getSessionKey(ctx)
	}
	if d.rootID == 0 {
		rootID := int64(-11)
		if d.rootPath != "" && d.rootPath != "/" {
			id, err := d.resolvePath(ctx, rootID, d.rootPath)
			if err != nil {
				return fmt.Errorf("189: resolve root path %q: %w", d.rootPath, err)
			}
			rootID = id
		}
		d.rootID = rootID
	}
	_, _, err := d.cl.listFiles(ctx, d.rootID)
	return err
}

func (d *Driver) Drop(ctx context.Context) error {
	return nil
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
}

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	id, err := strconv.ParseInt(parentID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("189: invalid id: %w", err)
	}
	folders, files, err := d.cl.listFiles(ctx, id)
	if err != nil {
		return nil, err
	}
	entries := make([]drive.Entry, 0, len(folders)+len(files))
	for _, f := range folders {
		entries = append(entries, drive.Entry{
			ID:       strconv.FormatInt(f.ID, 10),
			ParentID: parentID,
			Name:     f.Name,
			IsDir:    true,
			ModTime:  parseTime(f.LastOpTime),
		})
	}
	for _, f := range files {
		entries = append(entries, drive.Entry{
			ID:       strconv.FormatInt(f.ID, 10),
			ParentID: parentID,
			Name:     f.Name,
			Size:     f.Size,
			ModTime:  parseTime(f.LastOpTime),
		})
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("189: invalid offset or size")
	}
	fileID, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("189: invalid file id: %w", err)
	}
	rawURL, err := d.cl.getDownloadURL(ctx, fileID)
	if err != nil {
		return nil, err
	}
	rawURL, err = d.resolveDownloadURL(ctx, normalizeDownloadURL(rawURL))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	if size > 0 {
		end := offset + size - 1
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	} else if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Duration:  "0s",
			Request:   map[string]any{"offset": offset, "size": size, "range": req.Header.Get("Range")},
			Error:     err.Error(),
		})
		return nil, fmt.Errorf("189: read: %w", err)
	}
	if resp.StatusCode == http.StatusPartialContent || resp.StatusCode == http.StatusOK {
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Request:   map[string]any{"offset": offset, "size": size, "range": req.Header.Get("Range")},
		})
		return d.limiter.LimitDownload(ctx, resp.Body), nil
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset >= entry.Size {
		resp.Body.Close()
		return io.NopCloser(strings.NewReader("")), nil
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	d.cl.recordMetric(ctx, drive.MetricEvent{
		Operation: "download",
		Method:    req.Method,
		URL:       traceURL(req.URL),
		Status:    resp.StatusCode,
		Request:   map[string]any{"offset": offset, "size": size, "range": req.Header.Get("Range")},
		Response:  map[string]any{"body_snippet": responseSnippet(raw)},
	})
	return nil, fmt.Errorf("189: read: %s body=%q", resp.Status, responseSnippet(raw))
}

func (d *Driver) resolveDownloadURL(ctx context.Context, rawURL string) (string, error) {
	client := &http.Client{
		Jar: d.cl.hc.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp, err := client.Do(req)
	if err != nil {
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "resolve_download_url",
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Duration:  "0s",
			Error:     err.Error(),
		})
		return "", fmt.Errorf("189: resolve download url: %w", err)
	}
	defer resp.Body.Close()
	d.cl.recordMetric(ctx, drive.MetricEvent{
		Operation: "resolve_download_url",
		Method:    req.Method,
		URL:       traceURL(req.URL),
		Status:    resp.StatusCode,
		Response:  map[string]any{"location": normalizeDownloadURL(resp.Header.Get("Location"))},
	})
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusTemporaryRedirect {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", fmt.Errorf("189: resolve download url: redirect without location")
		}
		return normalizeDownloadURL(loc), nil
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		return rawURL, nil
	}
	return "", fmt.Errorf("189: resolve download url: %s", resp.Status)
}

func normalizeDownloadURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "//") {
		return "https:" + rawURL
	}
	if strings.HasPrefix(rawURL, "http://") {
		return "https://" + strings.TrimPrefix(rawURL, "http://")
	}
	return rawURL
}

func (d *Driver) resolvePath(ctx context.Context, parentID int64, p string) (int64, error) {
	p = path.Clean(p)
	if p == "" || p == "." || p == "/" {
		return parentID, nil
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	currentID := parentID
	for _, part := range parts {
		folders, _, err := d.cl.listFiles(ctx, currentID)
		if err != nil {
			return 0, err
		}
		found := false
		for _, folder := range folders {
			if folder.Name == part {
				currentID = folder.ID
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("189: path %q not found", p)
		}
	}
	return currentID, nil
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	parent, err := strconv.ParseInt(parentID, 10, 64)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("189: invalid parent id: %w", err)
	}
	size := source.Size()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	hashes, err := sourceUploadHashes(ctx, source, size, uploadPartSize)
	if err != nil {
		return drive.Entry{}, err
	}
	sessionKey := util.UploadSessionKey(parentID, name, size, hashes.FileMD5, hashes.SliceMD5)
	session, resumedSession := d.loadUploadSession(sessionKey)
	uploadFileID := session.UploadFileID
	fileDataExists := false
	if !resumedSession {
		uploadFileID, fileDataExists, err = d.cl.initUpload(ctx, parent, name, size, hashes.FileMD5, hashes.SliceMD5)
		if err != nil {
			return drive.Entry{}, err
		}
	}
	if !fileDataExists {
		if resumedSession {
			if session.CompletedParts == nil {
				session.CompletedParts = map[int]bool{}
			}
		} else {
			session = p189UploadSession{
				Key:            sessionKey,
				ParentID:       parentID,
				Name:           name,
				Size:           size,
				FileMD5:        hashes.FileMD5,
				SliceMD5:       hashes.SliceMD5,
				UploadFileID:   uploadFileID,
				PartSize:       uploadPartSize,
				CompletedParts: map[int]bool{},
			}
		}
		err := d.uploadParts(ctx, source, req.Progress, uploadFileID, hashes.Parts, session.CompletedParts, func(partNumber int) {
			session.CompletedParts[partNumber] = true
			d.saveUploadSession(session)
		})
		if err != nil {
			return drive.Entry{}, d.resumedUploadSessionError(resumedSession, sessionKey, err)
		}
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	if err := d.cl.commitUpload(ctx, uploadFileID, hashes.FileMD5, hashes.SliceMD5); err != nil {
		return drive.Entry{}, d.resumedUploadSessionError(resumedSession, sessionKey, err)
	}
	fileEntry, err := d.waitUploadedFile(ctx, parent, name)
	if err != nil {
		return drive.Entry{}, err
	}
	d.deleteUploadSession(sessionKey)
	return drive.Entry{
		ID:       strconv.FormatInt(fileEntry.ID, 10),
		ParentID: parentID,
		Name:     fileEntry.Name,
		Size:     fileEntry.Size,
		ModTime:  parseTime(fileEntry.LastOpTime),
	}, nil
}

func uploadPartInfo(part p189UploadPartMeta) string {
	return fmt.Sprintf("%d-%s", part.Number, part.MD5Base64)
}

func sourceUploadHashes(ctx context.Context, source drive.ReadOnlyFileSource, size, partSize int64) (p189UploadHashes, error) {
	if partSize <= 0 {
		return p189UploadHashes{}, fmt.Errorf("189: invalid upload part size")
	}
	if size <= partSize {
		fileMD5, err := sourceMD5Hex(ctx, source, size)
		if err != nil {
			return p189UploadHashes{}, err
		}
		part, err := uploadPartMeta(1, size, fileMD5)
		if err != nil {
			return p189UploadHashes{}, err
		}
		return p189UploadHashes{FileMD5: fileMD5, SliceMD5: fileMD5, Parts: []p189UploadPartMeta{part}}, nil
	}
	file, err := source.Open(ctx)
	if err != nil {
		return p189UploadHashes{}, fmt.Errorf("189: hash source open: %w", err)
	}
	defer file.Close()
	fileHash := md5.New()
	partCount := int((size + partSize - 1) / partSize)
	partHexes := make([]string, 0, partCount)
	parts := make([]p189UploadPartMeta, 0, partCount)
	buf := make([]byte, 1024*1024)
	for number := 1; number <= partCount; number++ {
		offset := int64(number-1) * partSize
		length := partSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		partHash := md5.New()
		reader := io.NewSectionReader(file, offset, length)
		written, err := io.CopyBuffer(io.MultiWriter(fileHash, partHash), reader, buf)
		if err != nil {
			return p189UploadHashes{}, fmt.Errorf("189: hash source read part %d: %w", number, err)
		}
		if written != length {
			return p189UploadHashes{}, fmt.Errorf("189: hash source part %d size mismatch: read %d, expected %d", number, written, length)
		}
		partMD5 := partHash.Sum(nil)
		partHex := strings.ToUpper(hex.EncodeToString(partMD5))
		partHexes = append(partHexes, partHex)
		parts = append(parts, p189UploadPartMeta{
			Number:    number,
			Size:      length,
			MD5Hex:    partHex,
			MD5Base64: base64.StdEncoding.EncodeToString(partMD5),
		})
	}
	fileMD5 := strings.ToUpper(hex.EncodeToString(fileHash.Sum(nil)))
	sliceSum := md5.Sum([]byte(strings.Join(partHexes, "\n")))
	return p189UploadHashes{
		FileMD5:  fileMD5,
		SliceMD5: strings.ToUpper(hex.EncodeToString(sliceSum[:])),
		Parts:    parts,
	}, nil
}

func uploadPartMeta(number int, size int64, md5Hex string) (p189UploadPartMeta, error) {
	sum, err := hex.DecodeString(md5Hex)
	if err != nil {
		return p189UploadPartMeta{}, fmt.Errorf("189: decode part md5: %w", err)
	}
	return p189UploadPartMeta{
		Number:    number,
		Size:      size,
		MD5Hex:    strings.ToUpper(md5Hex),
		MD5Base64: base64.StdEncoding.EncodeToString(sum),
	}, nil
}

func uploadSliceMD5(fileMD5, sliceMD5 string, size int64) string {
	if size <= uploadPartSize {
		return fileMD5
	}
	return sliceMD5
}

func applyUploadHeaders(req *http.Request, raw string) {
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		decoded = raw
	}
	for _, item := range strings.Split(decoded, "&") {
		if item == "" {
			continue
		}
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		req.Header.Set(key, value)
	}
}

func headerKeys(headers http.Header) []string {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (d *Driver) uploadParts(ctx context.Context, source drive.ReadOnlyFileSource, progress drive.UploadProgress, uploadFileID string, parts []p189UploadPartMeta, completed map[int]bool, markComplete func(int)) error {
	file, err := source.Open(ctx)
	if err != nil {
		return fmt.Errorf("189: upload source open: %w", err)
	}
	defer file.Close()
	for _, meta := range parts {
		if completed[meta.Number] {
			drive.ReportUploadProgress(progress, meta.Size)
			continue
		}
		urls, err := d.cl.uploadData(ctx, uploadFileID, uploadPartInfo(meta))
		if err != nil {
			return fmt.Errorf("189: get upload url part %d: %w", meta.Number, err)
		}
		part := urls["partNumber_"+strconv.Itoa(meta.Number)]
		if part.RequestURL == "" {
			return drive.NonRetryable(fmt.Errorf("189: upload urls missing partNumber_%d", meta.Number))
		}
		offset := int64(meta.Number-1) * uploadPartSize
		uploadBody := drive.NewUploadProgressReader(progress, io.NewSectionReader(file, offset, meta.Size))
		uploadBody = d.limiter.LimitUpload(ctx, uploadBody)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, part.RequestURL, uploadBody)
		if err != nil {
			return err
		}
		req.ContentLength = meta.Size
		applyUploadHeaders(req, part.RequestHeader)
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		start := time.Now()
		resp, err := d.cl.hc.Do(req)
		if err != nil {
			d.cl.recordMetric(ctx, drive.MetricEvent{
				Operation: "upload_part",
				Method:    req.Method,
				URL:       traceURL(req.URL),
				Duration:  time.Since(start).String(),
				Request:   map[string]any{"part_number": meta.Number, "bytes": meta.Size, "headers": headerKeys(req.Header)},
				Error:     err.Error(),
			})
			return fmt.Errorf("189: upload part %d: %w", meta.Number, err)
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			d.cl.recordMetric(ctx, drive.MetricEvent{
				Operation: "upload_part",
				Method:    req.Method,
				URL:       traceURL(req.URL),
				Status:    resp.StatusCode,
				Duration:  time.Since(start).String(),
				Request:   map[string]any{"part_number": meta.Number, "bytes": meta.Size, "headers": headerKeys(req.Header)},
				Response:  map[string]any{"body_snippet": responseSnippet(raw)},
			})
			err := fmt.Errorf("189: upload part %d: %s body=%q", meta.Number, resp.Status, responseSnippet(raw))
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
				err = drive.NonRetryable(err)
			}
			return err
		}
		resp.Body.Close()
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "upload_part",
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"part_number": meta.Number, "bytes": meta.Size, "headers": headerKeys(req.Header)},
		})
		if markComplete != nil {
			markComplete(meta.Number)
		}
	}
	return nil
}

func (d *Driver) loadUploadSession(key string) (p189UploadSession, bool) {
	session, ok := d.uploadSessionStore().Load(key)
	if session.CompletedParts == nil {
		session.CompletedParts = map[int]bool{}
	}
	return session, ok
}

func (d *Driver) saveUploadSession(session p189UploadSession) {
	d.uploadSessionStore().Save(session)
}

func (d *Driver) deleteUploadSession(key string) {
	d.uploadSessionStore().Delete(key)
}

func (d *Driver) uploadSessionStore() *util.UploadSessionStore[p189UploadSession] {
	return util.NewUploadSessionStore(util.UploadSessionStoreOptions[p189UploadSession]{
		Store:      d.stateStore,
		File:       p189UploadSessionStateFile,
		MaxAge:     p189UploadSessionMaxAge,
		MaxEntries: p189UploadSessionMaxEntries,
		Key: func(session p189UploadSession) string {
			return session.Key
		},
		Valid: func(key string, session p189UploadSession) bool {
			return session.Key != "" && session.UploadFileID != "" && len(session.CompletedParts) > 0
		},
		UpdatedAt: func(session p189UploadSession) time.Time {
			return session.SavedAt
		},
		Touch: func(session *p189UploadSession, now time.Time) {
			session.SavedAt = now
		},
		OnError: func(err error) {
			logging.L.Warnf("189: upload session state failed: %v", err)
		},
	})
}

func (d *Driver) resumedUploadSessionError(resumed bool, key string, err error) error {
	if resumed && (drive.IsNonRetryable(err) || invalidResumedUploadSession(err)) {
		d.deleteUploadSession(key)
		return fmt.Errorf("189: resumed upload session invalid, will retry from scratch: %v", err)
	}
	return err
}

func invalidResumedUploadSession(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "409") ||
		strings.Contains(s, "404") ||
		strings.Contains(s, "410") ||
		strings.Contains(s, "uploadFileId") ||
		strings.Contains(s, "InvalidUpload")
}

func (d *Driver) waitUploadedFile(ctx context.Context, parentID int64, name string) (File, error) {
	var last []File
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		_, files, err := d.cl.listFiles(ctx, parentID)
		if err != nil {
			return File{}, err
		}
		last = files
		for _, file := range files {
			if file.Name == name {
				return file, nil
			}
		}
	}
	names := make([]string, len(last))
	for i, file := range last {
		names[i] = file.Name
	}
	return File{}, fmt.Errorf("189: uploaded file %q not visible after commit; files=%v", name, names)
}

func sourceMD5Hex(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (string, error) {
	if sum, ok := drive.SourceHash(source, drive.HashMD5); ok {
		if len(sum) != md5.Size {
			return "", fmt.Errorf("189: source MD5 metadata has %d bytes, want %d", len(sum), md5.Size)
		}
		return strings.ToUpper(hex.EncodeToString(sum)), nil
	}
	body, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("189: hash source open: %w", err)
	}
	defer body.Close()
	hash := md5.New()
	written, err := io.Copy(hash, body)
	if err != nil {
		return "", fmt.Errorf("189: hash source read: %w", err)
	}
	if written != size {
		return "", fmt.Errorf("189: hash source size mismatch: read %d, expected %d", written, size)
	}
	return strings.ToUpper(hex.EncodeToString(hash.Sum(nil))), nil
}

func sourceSliceMD5Hex(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (string, error) {
	if size <= sliceMD5Size {
		if sum, ok := drive.SourceHash(source, drive.HashMD5); ok {
			if len(sum) != md5.Size {
				return "", fmt.Errorf("189: source MD5 metadata has %d bytes, want %d", len(sum), md5.Size)
			}
			return strings.ToUpper(hex.EncodeToString(sum)), nil
		}
	}
	sliceLen := size
	if sliceLen > sliceMD5Size {
		sliceLen = sliceMD5Size
	}
	body, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("189: slice hash source open: %w", err)
	}
	defer body.Close()
	buf := make([]byte, sliceLen)
	n, err := body.ReadAt(buf, 0)
	if err != nil && !(err == io.EOF && int64(n) == sliceLen) {
		return "", fmt.Errorf("189: slice hash source read: %w", err)
	}
	if int64(n) != sliceLen {
		return "", fmt.Errorf("189: slice hash source size mismatch: read %d, expected %d", n, sliceLen)
	}
	sum := md5.Sum(buf)
	return fmt.Sprintf("%X", sum), nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID string, name string) (drive.Entry, error) {
	parent, err := strconv.ParseInt(parentID, 10, 64)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("189: invalid parent id: %w", err)
	}
	id, err := d.cl.createFolder(ctx, parent, name)
	if err != nil {
		return drive.Entry{}, err
	}
	return drive.Entry{
		ID:       strconv.FormatInt(id, 10),
		ParentID: parentID,
		Name:     name,
		IsDir:    true,
	}, nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	id, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("189: invalid id: %w", err)
	}
	isFolder := 0
	if entry.IsDir {
		isFolder = 1
	}
	taskInfos, err := batchTaskInfos(batchTaskInfo{FileID: id, FileName: entry.Name, IsFolder: isFolder})
	if err != nil {
		return err
	}
	return d.cl.batchTask(ctx, "DELETE", taskInfos, "")
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	id, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("189: invalid id: %w", err)
	}
	return d.cl.rename(ctx, id, newName, entry.IsDir)
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	id, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("189: invalid id: %w", err)
	}
	isFolder := 0
	if entry.IsDir {
		isFolder = 1
	}
	taskInfos, err := batchTaskInfos(batchTaskInfo{FileID: id, FileName: entry.Name, IsFolder: isFolder})
	if err != nil {
		return err
	}
	return d.cl.batchTask(ctx, "MOVE", taskInfos, dstParentID)
}

func batchTaskInfos(infos ...batchTaskInfo) (string, error) {
	raw, err := json.Marshal(infos)
	if err != nil {
		return "", fmt.Errorf("189: encode batch task infos: %w", err)
	}
	return string(raw), nil
}

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	id, err := d.resolvePath(ctx, d.rootID, p)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	cap, err := d.cl.getCapacity(ctx)
	if err != nil {
		return drive.Space{}, err
	}
	return drive.Space{
		Total: cap.CloudCapacityInfo.TotalSize,
		Free:  cap.CloudCapacityInfo.FreeSize,
	}, nil
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	credentialSource := "none"
	switch {
	case d.cl.cookieValue() != "":
		credentialSource = d.cookieSource
		if credentialSource == "" {
			credentialSource = "cookie"
		}
	case d.cl.username != "":
		credentialSource = "username_password"
	}
	return drive.DebugSnapshot{
		Driver:      "189",
		Health:      drive.HealthLevelOK,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			drive.DebugStatRootID:   strconv.FormatInt(d.rootID, 10),
			drive.DebugStatRootPath: d.rootPath,
		},
		Extra: map[string]any{
			drive.DebugExtraCredentialSource:  credentialSource,
			drive.DebugExtraCredentialUpdated: d.cookieUpdated,
		},
	}, nil
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.cl.metricEvents(since), nil
}

func (d *Driver) loadCookieState() {
	if d.stateStore == nil {
		return
	}
	var state cookieState
	err := d.stateStore.LoadJSON("189_cookie.json", &state)
	if err != nil {
		return
	}
	if state.Cookie != "" {
		d.cl.mergeCookieHeader(state.Cookie)
		d.cookieSource = "state"
	}
	d.cookieUpdated = state.UpdatedAt
	d.cl.setPasswordReloginFailure(state.PasswordReloginFailedAt, state.PasswordReloginError)
}

func (d *Driver) saveUpdatedCookie(cookie string) {
	if cookie == "" {
		return
	}
	d.cookieSource = "response"
	d.cookieUpdated = time.Now()
	if d.stateStore == nil {
		return
	}
	if err := d.saveState(); err != nil {
		logging.L.Warnf("[189] save updated cookie state failed: %v", err)
	}
}

func (d *Driver) savePasswordReloginState(failedAt time.Time, lastError string) {
	if d.stateStore == nil {
		return
	}
	if err := d.saveState(); err != nil {
		logging.L.Warnf("[189] save password relogin state failed: %v", err)
	}
}

func (d *Driver) saveState() error {
	if d.stateStore == nil {
		return nil
	}
	d.cl.authMu.Lock()
	failedAt := d.cl.passwordReloginFailedAt
	lastError := d.cl.passwordReloginError
	d.cl.authMu.Unlock()
	return d.stateStore.SaveJSON("189_cookie.json", cookieState{
		Cookie:                  d.cl.cookieValue(),
		UpdatedAt:               d.cookieUpdated,
		PasswordReloginFailedAt: failedAt,
		PasswordReloginError:    lastError,
	})
}

func parseTime(s string) time.Time {
	t, err := time.ParseInLocation(timeFormat, s, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

var _ drive.StateStoreInstaller = (*Driver)(nil)
