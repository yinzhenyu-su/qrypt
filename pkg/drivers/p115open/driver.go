// Package p115open implements the 115 cloud drive driver on the official
// 115 open platform API (Bearer access_token + refresh_token).
//
// The refresh token is obtained by authorizing an app on open.115.com (for
// example by scanning a PKCE device-code QR with the 115 app). The driver
// auto-refreshes the access token and persists rotated tokens in the state
// store, so a static refresh_token in the config stays valid across restarts.
package p115open

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"golang.org/x/time/rate"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"
)

// downloadUserAgent is the User-Agent used to request download URLs and fetch
// the resulting CDN links. 115 returns f=1 links (user-agent only) for
// non-browser UAs; browser-like UAs yield f=3 links that additionally require
// a cookie from the downurl response, which the open API does not expose.
const downloadUserAgent = "qrypt/0.1"

// preHashSize is the size of the file prefix whose SHA1 participates in the
// 115 rapid-upload fingerprint.
const preHashSize = 128 * 1024

const tokenStateFile = "115_open_tokens.json"

type Driver struct {
	drive.UnsupportedOperations
	cl                 *sdk.Client
	rootID             string
	rootPath           string
	limitRate          float64
	limiter            *rate.Limiter
	metrics            *util.Buffer
	stateStore         drive.StateStore
	accessToken        string
	refreshToken       string
	configRefreshToken string
	tokenSource        string
	tokenUpdated       time.Time
	tokenMu            sync.Mutex
	userID             int64
	debugMu            sync.Mutex
	lastError          string
	instantUploads     atomic.Int64
	bandwidthLimiter   *drive.BandwidthLimiter
	ossMu              sync.Mutex
	ossToken           *sdk.UploadGetTokenResp
	ossTokenAt         time.Time
}

type tokenState struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// ConfigRefreshToken records the config refresh token that produced this
	// state, so a rotated pair (state newer than config) can be distinguished
	// from a user-swapped config token (config newer than state).
	ConfigRefreshToken string    `json:"config_refresh_token,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

func init() {
	drive.Register("115_open", func(params drive.Params) (drive.Driver, error) {
		if raw, err := strconv.ParseFloat(params["limit_rate"], 64); err == nil && raw > 0 {
			return New(Options{
				AccessToken:  params["access_token"],
				RefreshToken: params["refresh_token"],
				RootPath:     params["root_path"],
				LimitRate:    raw,
			}), nil
		}
		return New(Options{
			AccessToken:  params["access_token"],
			RefreshToken: params["refresh_token"],
			RootPath:     params["root_path"],
			LimitRate:    2,
		}), nil
	},
		drive.ParamDef{
			Name:        "refresh_token",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "115 open platform refresh token (obtained by authorizing an app on open.115.com)",
			Example:     "eg2t4.<hex>.<hex>",
		},
		drive.ParamDef{
			Name:        "access_token",
			Type:        "string",
			Secret:      true,
			Description: "115 open platform access token (optional; refreshed automatically when missing or expired)",
			Example:     "eg2t4.<hex>.<hex>",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Virtual root path, resolved to the provider folder ID at startup",
			Default:     "/",
			Example:     "/qrypt",
		},
		drive.ParamDef{
			Name:        "limit_rate",
			Type:        "int",
			Description: "Limit all API requests to N per second",
			Default:     "2",
		},
	)
}

type Options struct {
	AccessToken  string
	RefreshToken string
	RootID       string
	RootPath     string
	LimitRate    float64
}

func New(opts Options) *Driver {
	return &Driver{
		rootID:             opts.RootID,
		rootPath:           opts.RootPath,
		limitRate:          opts.LimitRate,
		accessToken:        opts.AccessToken,
		refreshToken:       opts.RefreshToken,
		configRefreshToken: opts.RefreshToken,
		tokenSource:        "config",
		metrics:            util.NewBuffer(500),
	}
}

func (d *Driver) Init(ctx context.Context) error {
	d.loadTokenState()
	if d.refreshToken == "" && d.accessToken == "" {
		return fmt.Errorf("115_open: Init: missing refresh_token")
	}
	if d.limitRate > 0 {
		d.limiter = rate.NewLimiter(rate.Limit(d.limitRate), 1)
	}
	d.cl = sdk.New(
		sdk.WithAccessToken(d.accessToken),
		sdk.WithRefreshToken(d.refreshToken),
		sdk.WithOnRefreshToken(func(accessToken, refreshToken string) {
			d.saveTokens(accessToken, refreshToken, "refresh")
		}),
	)
	d.cl.SetUserAgent(downloadUserAgent)
	if err := d.waitLimit(ctx); err != nil {
		return err
	}
	info, err := d.cl.UserInfo(ctx)
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: user info: %v", err))
		return fmt.Errorf("115_open: user info: %w", err)
	}
	if info != nil {
		d.userID = info.UserID
	}
	d.saveTokens(d.accessToken, d.refreshToken, d.tokenSource)
	if d.rootID == "" {
		d.rootID = "0"
	}
	if d.rootPath != "" && d.rootPath != "/" {
		rootID, err := d.resolvePathFrom(ctx, d.rootID, d.rootPath)
		if err != nil {
			return fmt.Errorf("115_open: resolve root_path %q: %w", d.rootPath, err)
		}
		d.rootID = rootID
	}
	return nil
}

func (d *Driver) Drop(context.Context) error {
	return nil
}

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.metrics.Events(since), nil
}

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	if err := d.waitLimit(ctx); err != nil {
		return nil, err
	}
	dirID := d.resolveID(parentID)
	var entries []drive.Entry
	err := d.recordSDK(ctx, "list", map[string]any{"parent_id": dirID}, func() error {
		var err error
		entries, err = d.getFiles(ctx, dirID)
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: list %q: %v", parentID, err))
		return nil, err
	}
	return entries, nil
}

func (d *Driver) getFiles(ctx context.Context, dirID string) ([]drive.Entry, error) {
	const pageSize = 1000
	var entries []drive.Entry
	offset := int64(0)
	for {
		resp, err := d.cl.GetFiles(ctx, &sdk.GetFilesReq{
			CID:     dirID,
			Limit:   pageSize,
			Offset:  offset,
			ShowDir: true,
		})
		if err != nil {
			return nil, err
		}
		for i := range resp.Data {
			entries = append(entries, entryFromFile(resp.Data[i]))
		}
		if len(resp.Data) == 0 || len(entries) >= int(resp.Count) {
			break
		}
		offset += int64(len(resp.Data))
	}
	return entries, nil
}

func entryFromFile(f sdk.GetFilesResp_File) drive.Entry {
	return drive.Entry{
		ID:        f.Fid,
		ParentID:  f.Pid,
		Name:      f.Fn,
		Size:      f.FS,
		IsDir:     f.Fc == "0",
		ModTime:   time.Unix(f.Upt, 0),
		UpdatedAt: time.Unix(f.Uet, 0),
		Extra:     f,
	}
}

func (d *Driver) Read(ctx context.Context, e drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("115_open: invalid offset or size")
	}
	rawSize := rawEntrySize(e)
	if !e.IsDir && rawSize > 0 && offset >= rawSize {
		return io.NopCloser(strings.NewReader("")), nil
	}
	if err := d.waitLimit(ctx); err != nil {
		return nil, err
	}
	pickCode, err := d.pickCode(ctx, e)
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: read pick_code %q: %v", e.ID, err))
		return nil, err
	}
	var urlStr string
	err = d.recordSDK(ctx, "down_url", map[string]any{"id": e.ID, "name": e.Name, "offset": offset, "size": size}, func() error {
		urlStr, err = d.downloadURL(ctx, pickCode, e.ID)
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: download info %q: %v", e.ID, err))
		return nil, fmt.Errorf("115_open: download info: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", downloadUserAgent)
	if size > 0 {
		end := offset + size - 1
		if rawSize > 0 && end >= rawSize {
			end = rawSize - 1
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	} else if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.metrics.Record(ctx, drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       util.URL(req.URL),
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"id": e.ID, "offset": offset, "size": size, "range": req.Header.Get("Range")},
			Error:     err.Error(),
		})
		d.setLastError(fmt.Sprintf("115_open: read %q: %v", e.ID, err))
		return nil, fmt.Errorf("115_open: read: %w", err)
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		d.metrics.Record(ctx, drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       util.URL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"id": e.ID, "offset": offset, "size": size, "range": req.Header.Get("Range")},
		})
		return d.bandwidthLimiter.LimitDownload(ctx, resp.Body), nil
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && rawSize > 0 && offset >= rawSize {
		resp.Body.Close()
		return io.NopCloser(strings.NewReader("")), nil
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	d.metrics.Record(ctx, drive.MetricEvent{
		Operation: "download",
		Method:    req.Method,
		URL:       util.URL(req.URL),
		Status:    resp.StatusCode,
		Duration:  time.Since(start).String(),
		Request:   map[string]any{"id": e.ID, "offset": offset, "size": size, "range": req.Header.Get("Range")},
		Response:  map[string]any{"body_snippet": util.Snippet(raw)},
	})
	err = fmt.Errorf("115_open: read: %s body=%q", resp.Status, util.Snippet(raw))
	d.setLastError(err.Error())
	return nil, err
}

func (d *Driver) downloadURL(ctx context.Context, pickCode, fileID string) (string, error) {
	resp, err := d.cl.DownURL(ctx, pickCode, downloadUserAgent)
	if err != nil {
		return "", err
	}
	if len(resp) == 0 {
		return "", fmt.Errorf("115_open: download info missing url")
	}
	if u, ok := resp[fileID]; ok && u.URL.URL != "" {
		return u.URL.URL, nil
	}
	for _, u := range resp {
		if u.URL.URL != "" {
			return u.URL.URL, nil
		}
	}
	return "", fmt.Errorf("115_open: download info missing url")
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := d.resolveID(req.ParentID), req.Name, req.Source
	if source == nil {
		return drive.Entry{}, drive.NonRetryable(fmt.Errorf("115_open: upload source is nil"))
	}
	body, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("115_open: upload source open: %w", err)
	}
	defer body.Close()
	fullSHA1 := ""
	if sum, ok := drive.SourceHash(source, drive.HashSHA1); ok {
		fullSHA1 = strings.ToUpper(hex.EncodeToString(sum))
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	err = d.recordSDK(ctx, "upload", map[string]any{"parent_id": parentID, "name": name, "size": source.Size()}, func() error {
		return d.uploadSource(ctx, parentID, name, source.Size(), fullSHA1, body, req.Progress)
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: upload %q: %v", name, err))
		return drive.Entry{}, fmt.Errorf("115_open: upload: %w", err)
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCompleted)
	entry, err := d.waitUploadedFile(ctx, parentID, name, source)
	if err != nil {
		d.setLastError(err.Error())
		return drive.Entry{}, err
	}
	return entry, nil
}

func (d *Driver) uploadSource(ctx context.Context, parentID, name string, size int64, fullSHA1 string, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	if fullSHA1 == "" {
		var err error
		fullSHA1, err = d.hashAll(body)
		if err != nil {
			return err
		}
	}
	preSHA1, err := d.prefixSHA1(size, body)
	if err != nil {
		return err
	}
	initResp, err := d.uploadInit(ctx, name, size, parentID, fullSHA1, preSHA1, "", "")
	if err != nil {
		return err
	}
	if initResp.Status == 2 {
		d.instantUploads.Add(1)
		return nil
	}
	if initResp.Status == 6 || initResp.Status == 7 || initResp.Status == 8 {
		signVal, err := d.hashSignRange(body, initResp.SignCheck)
		if err != nil {
			return err
		}
		initResp, err = d.uploadInit(ctx, name, size, parentID, fullSHA1, preSHA1, initResp.SignKey, signVal)
		if err != nil {
			return err
		}
		if initResp.Status == 2 {
			d.instantUploads.Add(1)
			return nil
		}
	}
	if initResp.Bucket == "" || initResp.Object == "" {
		return drive.NonRetryable(fmt.Errorf("115_open: upload init missing bucket/object (status=%d)", initResp.Status))
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return d.uploadToOSS(ctx, parentID, name, size, fullSHA1, initResp, body, progress)
}

func (d *Driver) hashAll(body io.ReadSeeker) (string, error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hasher := sha1.New()
	if _, err := io.Copy(hasher, body); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(hasher.Sum(nil))), nil
}

func (d *Driver) prefixSHA1(size int64, body io.ReadSeeker) (string, error) {
	prefixSize := int64(preHashSize)
	if size < prefixSize {
		prefixSize = size
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hasher := sha1.New()
	if _, err := io.CopyN(hasher, body, prefixSize); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(hasher.Sum(nil))), nil
}

// hashSignRange hashes the byte range [start,end] specified by signCheck
// (format "start-end") for the 115 two-way rapid-upload verification.
func (d *Driver) hashSignRange(body io.ReadSeeker, signCheck string) (string, error) {
	parts := strings.SplitN(signCheck, "-", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("115_open: bad sign_check %q", signCheck)
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("115_open: bad sign_check %q", signCheck)
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", fmt.Errorf("115_open: bad sign_check %q", signCheck)
	}
	if end < start {
		return "", fmt.Errorf("115_open: bad sign_check %q", signCheck)
	}
	if _, err := body.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	hasher := sha1.New()
	if _, err := io.CopyN(hasher, body, end-start+1); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(hasher.Sum(nil))), nil
}

func (d *Driver) uploadInit(ctx context.Context, name string, size int64, parentID, fullSHA1, preSHA1, signKey, signVal string) (*sdk.UploadInitResp, error) {
	return d.cl.UploadInit(ctx, &sdk.UploadInitReq{
		FileName: name,
		FileSize: size,
		Target:   parentID,
		FileID:   fullSHA1,
		PreID:    preSHA1,
		SignKey:  signKey,
		SignVal:  signVal,
	})
}

func (d *Driver) callbackOf(initResp *sdk.UploadInitResp) (callback, callbackVar string) {
	if initResp == nil || initResp.Callback.Value == nil {
		return "", ""
	}
	return initResp.Callback.Value.Callback, initResp.Callback.Value.CallbackVar
}

func (d *Driver) uploadToOSS(ctx context.Context, parentID, name string, size int64, sha1Hex string, initResp *sdk.UploadInitResp, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	ossToken, err := d.ossTokenFor(ctx)
	if err != nil {
		return fmt.Errorf("115_open: get oss token: %w", err)
	}
	ossClient, err := oss.New(
		ossToken.Endpoint,
		ossToken.AccessKeyId,
		ossToken.AccessKeySecret,
		oss.SecurityToken(ossToken.SecurityToken),
		oss.EnableMD5(true),
		oss.EnableCRC(true),
	)
	if err != nil {
		return fmt.Errorf("115_open: create oss client: %w", err)
	}
	bucket, err := ossClient.Bucket(initResp.Bucket)
	if err != nil {
		return fmt.Errorf("115_open: open oss bucket: %w", err)
	}
	if size < calPartSize(size) {
		uploadBody := drive.NewUploadProgressReader(progress, body)
		uploadBody = d.bandwidthLimiter.LimitUpload(ctx, uploadBody)
		start := time.Now()
		err := bucket.PutObject(initResp.Object, contextReader{ctx: ctx, reader: uploadBody}, d.ossCallbackOptions(initResp, nil)...)
		d.metrics.Record(ctx, uploadMetric("oss_upload", size, start, err))
		return err
	}
	return d.uploadMultipart(ctx, parentID, name, size, sha1Hex, initResp, bucket, body, progress)
}

func (d *Driver) ossCallbackOptions(initResp *sdk.UploadInitResp, bodyBytes *[]byte) []oss.Option {
	callback, callbackVar := d.callbackOf(initResp)
	options := make([]oss.Option, 0, 3)
	if callback != "" {
		options = append(options, oss.Callback(base64.StdEncoding.EncodeToString([]byte(callback))))
	}
	if callbackVar != "" {
		options = append(options, oss.CallbackVar(base64.StdEncoding.EncodeToString([]byte(callbackVar))))
	}
	if bodyBytes != nil {
		options = append(options, oss.CallbackResult(bodyBytes))
	}
	return options
}

func (d *Driver) uploadMultipart(ctx context.Context, parentID, name string, size int64, sha1Hex string, initResp *sdk.UploadInitResp, bucket *oss.Bucket, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	callback, callbackVar := d.callbackOf(initResp)
	sessionKey := util.UploadSessionKey(parentID, name, size, strings.ToUpper(sha1Hex))
	session, resumed := d.loadUploadSession(sessionKey)
	if resumed {
		logging.L.InfofEvery("115_open.upload_resume", time.Second, "[115_open] upload resume name=%q upload_id=%q completed_parts=%d", name, session.UploadID, len(session.Parts))
	} else {
		session = p115OpenUploadSession{
			Key:       sessionKey,
			ParentID:  parentID,
			Name:      name,
			Size:      size,
			SHA1:      strings.ToUpper(sha1Hex),
			Bucket:    initResp.Bucket,
			Object:    initResp.Object,
			PartSize:  calPartSize(size),
			Callback:  callback,
			CallbackV: callbackVar,
		}
	}
	partSize := session.PartSize
	if partSize <= 0 {
		partSize = calPartSize(size)
		session.PartSize = partSize
	}
	imur := oss.InitiateMultipartUploadResult{
		Bucket:   session.Bucket,
		Key:      session.Object,
		UploadID: session.UploadID,
	}
	if imur.UploadID == "" {
		initResult, err := bucket.InitiateMultipartUpload(session.Object, oss.Sequential())
		if err != nil {
			return fmt.Errorf("115_open: initiate multipart upload: %w", err)
		}
		imur.UploadID = initResult.UploadID
		session.UploadID = initResult.UploadID
		d.saveUploadSession(session)
	}
	partsByNumber := session.partsByNumber()
	uploadParts := make([]oss.UploadPart, 0, len(p115OpenUploadPartRanges(size, partSize)))
	for _, part := range p115OpenUploadPartRanges(size, partSize) {
		if completed, ok := partsByNumber[part.Number]; ok {
			drive.ReportUploadProgress(progress, part.Size)
			uploadParts = append(uploadParts, completed)
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		reader := io.NewSectionReader(body, part.Offset, part.Size)
		uploadBody := drive.NewUploadProgressReader(progress, reader)
		uploadBody = d.bandwidthLimiter.LimitUpload(ctx, uploadBody)
		uploadBody = contextReader{ctx: ctx, reader: uploadBody}
		start := time.Now()
		uploadedPart, err := bucket.UploadPart(imur, uploadBody, part.Size, part.Number)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
		}
		d.metrics.Record(ctx, uploadPartMetric("oss_upload_part", part, start, err))
		if err != nil {
			return fmt.Errorf("115_open: upload part %d: %w", part.Number, err)
		}
		partsByNumber[part.Number] = uploadedPart
		uploadParts = append(uploadParts, uploadedPart)
		session.Parts = append(session.Parts, ossPart{Number: uploadedPart.PartNumber, ETag: uploadedPart.ETag})
		d.saveUploadSession(session)
	}
	sort.Slice(uploadParts, func(i, j int) bool {
		return uploadParts[i].PartNumber < uploadParts[j].PartNumber
	})
	drive.ReportUploadPhase(progress, drive.UploadPhaseCommitting)
	var bodyBytes []byte
	_, err := bucket.CompleteMultipartUpload(imur, uploadParts, d.ossCallbackOptions(initResp, &bodyBytes)...)
	if err != nil {
		return fmt.Errorf("115_open: complete multipart upload: %w", err)
	}
	d.deleteUploadSession(sessionKey)
	return nil
}

func uploadMetric(operation string, bytes int64, started time.Time, err error) drive.MetricEvent {
	event := drive.MetricEvent{
		Layer:     "driver.oss",
		Operation: operation,
		Duration:  time.Since(started).String(),
		Request:   map[string]any{"bytes": bytes},
	}
	if err != nil {
		event.Error = err.Error()
	}
	return event
}

func uploadPartMetric(operation string, part p115OpenUploadPartRange, started time.Time, err error) drive.MetricEvent {
	event := drive.MetricEvent{
		Layer:     "driver.oss",
		Operation: operation,
		Duration:  time.Since(started).String(),
		Request: map[string]any{
			"part_number": part.Number,
			"bytes":       part.Size,
			"offset":      part.Offset,
		},
	}
	if err != nil {
		event.Error = err.Error()
	}
	return event
}

type p115OpenUploadPartRange struct {
	Number int
	Offset int64
	Size   int64
}

func p115OpenUploadPartRanges(size, partSize int64) []p115OpenUploadPartRange {
	if size < 0 || partSize <= 0 {
		return nil
	}
	if size == 0 {
		return []p115OpenUploadPartRange{{Number: 1}}
	}
	parts := make([]p115OpenUploadPartRange, 0, int((size+partSize-1)/partSize))
	for offset, number := int64(0), 1; offset < size; offset, number = offset+partSize, number+1 {
		length := partSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		parts = append(parts, p115OpenUploadPartRange{Number: number, Offset: offset, Size: length})
	}
	return parts
}

// calPartSize picks an OSS multipart part size that keeps the total part
// count under the 10000-part limit for very large files.
func calPartSize(fileSize int64) int64 {
	var partSize int64 = 20 * 1024 * 1024
	switch {
	case fileSize > 1<<40: // over 1TB
		partSize = 5 << 30
	case fileSize > 768<<30:
		partSize = 109951163
	case fileSize > 512<<30:
		partSize = 82463373
	case fileSize > 384<<30:
		partSize = 54975582
	case fileSize > 256<<30:
		partSize = 41231687
	case fileSize > 128<<30:
		partSize = 27487791
	}
	return partSize
}

func (d *Driver) ossTokenFor(ctx context.Context) (*sdk.UploadGetTokenResp, error) {
	d.ossMu.Lock()
	defer d.ossMu.Unlock()
	if d.ossToken != nil && time.Since(d.ossTokenAt) < 5*time.Minute {
		return d.ossToken, nil
	}
	token, err := d.cl.UploadGetToken(ctx)
	if err != nil {
		return nil, err
	}
	d.ossToken = token
	d.ossTokenAt = time.Now()
	return token, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err != nil {
		return n, err
	}
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID string, name string) (drive.Entry, error) {
	if err := d.waitLimit(ctx); err != nil {
		return drive.Entry{}, err
	}
	parentID = d.resolveID(parentID)
	var resp *sdk.MkdirResp
	err := d.recordSDK(ctx, "mkdir", map[string]any{"parent_id": parentID, "name": name}, func() error {
		var err error
		resp, err = d.cl.Mkdir(ctx, parentID, name)
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: mkdir %q: %v", name, err))
		return drive.Entry{}, err
	}
	if resp == nil || resp.FileID == "" {
		return drive.Entry{}, fmt.Errorf("115_open: mkdir %q: empty response", name)
	}
	return drive.Entry{ID: resp.FileID, ParentID: parentID, Name: name, IsDir: true}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	if err := d.waitLimit(ctx); err != nil {
		return err
	}
	dstParentID = d.resolveID(dstParentID)
	err := d.recordSDK(ctx, "move", map[string]any{"id": entry.ID, "dst_parent_id": dstParentID}, func() error {
		_, err := d.cl.Move(ctx, &sdk.MoveReq{FileIDs: entry.ID, ToCid: dstParentID})
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: move %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	if err := d.waitLimit(ctx); err != nil {
		return err
	}
	err := d.recordSDK(ctx, "rename", map[string]any{"id": entry.ID, "new_name": newName}, func() error {
		_, err := d.cl.UpdateFile(ctx, &sdk.UpdateFileReq{FileID: entry.ID, FileName: newName})
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: rename %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	if err := d.waitLimit(ctx); err != nil {
		return err
	}
	err := d.recordSDK(ctx, "remove", map[string]any{"id": entry.ID, "is_dir": entry.IsDir}, func() error {
		_, err := d.cl.DelFile(ctx, &sdk.DelFileReq{FileIDs: entry.ID, ParentID: entry.ParentID})
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: remove %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	if err := d.waitLimit(ctx); err != nil {
		return drive.Space{}, err
	}
	var info *sdk.UserInfoResp
	err := d.recordSDK(ctx, "space", nil, func() error {
		var err error
		info, err = d.cl.UserInfo(ctx)
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: space: %v", err))
		return drive.Space{}, err
	}
	if info == nil {
		return drive.Space{}, fmt.Errorf("115_open: space: empty response")
	}
	total, totalErr := info.RtSpaceInfo.AllTotal.Size.Int64()
	free, freeErr := info.RtSpaceInfo.AllRemain.Size.Int64()
	if totalErr != nil || freeErr != nil {
		return drive.Space{}, fmt.Errorf("115_open: space: parse sizes failed")
	}
	return drive.Space{Total: total, Free: free}, nil
}

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	return d.resolvePathFrom(ctx, d.rootID, p)
}

// ResolveRemoteName maps a plaintext name to the backend remote name. The
// 115 open API stores files under their plaintext name, so this is the
// identity mapping.
func (d *Driver) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	return drive.RemoteNameInfo{PlainName: plainName, RemoteName: plainName}, nil
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	d.debugMu.Lock()
	lastError := d.lastError
	d.debugMu.Unlock()
	extra := map[string]any{
		drive.DebugExtraCredentialSource:   d.tokenSource,
		drive.DebugExtraInstantUploadCount: d.instantUploads.Load(),
	}
	if !d.tokenUpdated.IsZero() {
		extra[drive.DebugExtraCredentialUpdated] = d.tokenUpdated
	}
	health := drive.HealthLevelOK
	if lastError != "" {
		health = drive.HealthLevelDegraded
		extra[drive.DebugExtraLastError] = lastError
	}
	stats := map[string]any{
		drive.DebugStatRootID:   d.rootID,
		drive.DebugStatRootPath: d.rootPath,
	}
	if d.userID != 0 {
		stats["user_id"] = d.userID
	}
	return drive.DebugSnapshot{
		Driver:      "115_open",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats:       stats,
		Extra:       extra,
	}, nil
}

func (d *Driver) waitLimit(ctx context.Context) error {
	if d.limiter != nil {
		return d.limiter.Wait(ctx)
	}
	return nil
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.bandwidthLimiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) pickCode(ctx context.Context, entry drive.Entry) (string, error) {
	switch f := drive.EntryRawExtra(entry).(type) {
	case sdk.GetFilesResp_File:
		if f.Pc != "" {
			return f.Pc, nil
		}
	case *sdk.GetFilesResp_File:
		if f != nil && f.Pc != "" {
			return f.Pc, nil
		}
	}
	var info *sdk.GetFolderInfoResp
	err := d.recordSDK(ctx, "get_file", map[string]any{"id": entry.ID}, func() error {
		var err error
		info, err = d.cl.GetFolderInfo(ctx, entry.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	if info == nil || info.PickCode == "" {
		return "", fmt.Errorf("115_open: file %q missing pick_code", entry.ID)
	}
	return info.PickCode, nil
}

func (d *Driver) waitUploadedFile(ctx context.Context, parentID, name string, source drive.ReadOnlyFileSource) (drive.Entry, error) {
	sha1Hex := ""
	if sum, ok := drive.SourceHash(source, drive.HashSHA1); ok {
		sha1Hex = strings.ToUpper(hex.EncodeToString(sum))
	}
	var last []drive.Entry
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return drive.Entry{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		entries, err := d.List(ctx, parentID)
		if err != nil {
			return drive.Entry{}, err
		}
		last = entries
		for _, entry := range entries {
			if entry.Name != name || entry.IsDir || entry.Size != source.Size() {
				continue
			}
			if sha1Hex == "" || entrySHA1(entry) == sha1Hex {
				return entry, nil
			}
		}
	}
	names := make([]string, len(last))
	for i, entry := range last {
		names[i] = entry.Name
	}
	return drive.Entry{}, fmt.Errorf("115_open: uploaded file %q not visible after upload; files=%v", name, names)
}

func entrySHA1(entry drive.Entry) string {
	switch f := drive.EntryRawExtra(entry).(type) {
	case sdk.GetFilesResp_File:
		return strings.ToUpper(f.Sha1)
	case *sdk.GetFilesResp_File:
		if f != nil {
			return strings.ToUpper(f.Sha1)
		}
	}
	return ""
}

func rawEntrySize(entry drive.Entry) int64 {
	switch f := drive.EntryRawExtra(entry).(type) {
	case sdk.GetFilesResp_File:
		return f.FS
	case *sdk.GetFilesResp_File:
		if f != nil {
			return f.FS
		}
	}
	return entry.Size
}

func (d *Driver) resolveID(fileID string) string {
	if fileID == "" || fileID == "0" || fileID == "/" {
		return d.rootID
	}
	return fileID
}

func (d *Driver) resolvePathFrom(ctx context.Context, rootID, p string) (string, error) {
	currentID := d.resolveID(rootID)
	p = strings.Trim(p, "/")
	if p == "" {
		return currentID, nil
	}
	for _, segment := range strings.Split(p, "/") {
		entries, err := d.List(ctx, currentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.IsDir && entry.Name == segment {
				currentID = entry.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%w: directory %q not found under %q", drive.ErrNotFound, segment, p)
		}
	}
	return currentID, nil
}

func (d *Driver) recordSDK(ctx context.Context, operation string, request map[string]any, fn func() error) error {
	start := time.Now()
	err := fn()
	event := drive.MetricEvent{
		Layer:     "driver.sdk",
		Operation: operation,
		Duration:  time.Since(start).String(),
		Request:   request,
	}
	if err != nil {
		event.Error = err.Error()
	}
	d.metrics.Record(ctx, event)
	return err
}

func (d *Driver) setLastError(value string) {
	d.debugMu.Lock()
	defer d.debugMu.Unlock()
	d.lastError = value
}

// loadTokenState restores previously rotated tokens from the state store.
//
// The state refresh token wins when it was derived from the current config
// token (token rotation) or when no config token is present, because 115
// rotates refresh tokens on every refresh and invalidates the old one. A
// config token that differs from the token the state was derived from is
// treated as an explicit account/app switch and replaces state.
func (d *Driver) loadTokenState() {
	if d.stateStore == nil {
		return
	}
	var state tokenState
	if err := d.stateStore.LoadJSON(tokenStateFile, &state); err != nil {
		return
	}
	// States written before the source marker was recorded are treated as
	// derived from any config token (they are more recent than the config).
	stateDerived := state.ConfigRefreshToken == "" || state.ConfigRefreshToken == d.configRefreshToken
	if state.RefreshToken != "" && (d.refreshToken == "" || stateDerived) {
		d.refreshToken = state.RefreshToken
		if d.accessToken == "" {
			d.accessToken = state.AccessToken
		}
		d.tokenSource = "state"
	}
	if !state.UpdatedAt.IsZero() {
		d.tokenUpdated = state.UpdatedAt
	}
}

// saveTokens persists the current token pair. Rotated tokens are written
// through the SDK's OnRefreshToken callback.
func (d *Driver) saveTokens(accessToken, refreshToken, source string) {
	if accessToken == "" || refreshToken == "" {
		return
	}
	d.tokenMu.Lock()
	d.accessToken = accessToken
	d.refreshToken = refreshToken
	d.tokenSource = source
	d.tokenUpdated = time.Now()
	d.tokenMu.Unlock()
	if d.stateStore == nil {
		return
	}
	if err := d.stateStore.SaveJSON(tokenStateFile, tokenState{
		AccessToken:        accessToken,
		RefreshToken:       refreshToken,
		ConfigRefreshToken: d.configRefreshToken,
		UpdatedAt:          d.tokenUpdated,
	}); err != nil {
		logging.L.Warnf("[115_open] save token state failed: %v", err)
	}
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.BandwidthLimitInstaller = (*Driver)(nil)
var _ drive.StateStoreInstaller = (*Driver)(nil)
