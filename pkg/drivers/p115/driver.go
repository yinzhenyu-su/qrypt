// Package p115 implements the 115 cloud drive driver.
package p115

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	cipher "github.com/SheltonZhu/115driver/pkg/crypto/ec115"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/go-resty/resty/v2"
	"golang.org/x/time/rate"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const defaultAppVer = "35.6.0.3"
const md5Salt = "Qclm8MGWUv59TnrR0XPg"

var appVer = defaultAppVer
var loginCheckRetryDelays = []time.Duration{1 * time.Second, 2 * time.Second}

type Driver struct {
	drive.UnsupportedOperations
	cl               *driver115.Pan115Client
	rootID           string
	rootPath         string
	cookies          string
	limitRate        float64
	limiter          *rate.Limiter
	bandwidthLimiter *drive.BandwidthLimiter
	httpClient       *http.Client
	metrics          *util.Buffer
	stateStore       drive.StateStore
	cookieSource     string
	cookieUpdated    time.Time
	debugMu          sync.Mutex
	lastError        string
}

type cookieState struct {
	Cookie    string    `json:"cookie,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type p115UploadSessionState struct {
	Version  int                          `json:"version"`
	Sessions map[string]p115UploadSession `json:"sessions,omitempty"`
}

type p115UploadSession struct {
	Key       string    `json:"key"`
	ParentID  string    `json:"parent_id"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	SHA1      string    `json:"sha1"`
	Bucket    string    `json:"bucket"`
	Object    string    `json:"object"`
	UploadID  string    `json:"upload_id"`
	PartSize  int64     `json:"part_size"`
	Parts     []ossPart `json:"parts,omitempty"`
	Callback  string    `json:"callback,omitempty"`
	CallbackV string    `json:"callback_var,omitempty"`
	SavedAt   time.Time `json:"saved_at"`
}

type ossPart struct {
	Number int    `json:"number"`
	ETag   string `json:"etag"`
}

const p115UploadSessionStateFile = "115_upload_sessions.json"
const p115UploadSessionMaxAge = 24 * time.Hour
const p115UploadSessionMaxEntries = 1024
const p115MultipartPartSize = 16 * 1024 * 1024
const p115MultipartMinSize = p115MultipartPartSize

func init() {
	drive.Register("115", func(params drive.Params) (drive.Driver, error) {
		cookie := params["cookie"]
		return New(Options{
			Cookie:    cookie,
			RootPath:  params["root_path"],
			LimitRate: 2,
		}), nil
	},
		drive.ParamDef{
			Name:        "cookie",
			Type:        "string",
			Secret:      true,
			Description: "115 cloud drive authentication cookie (required on first run; later can be loaded from state)",
			Example:     "k1=v1; k2=v2",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Virtual root path, resolved to the provider folder ID at startup",
			Default:     "/",
			Example:     "/qrypt",
		},
	)
}

type Options struct {
	Cookie    string
	RootID    string
	RootPath  string
	LimitRate float64
}

func New(opts Options) *Driver {
	return &Driver{
		rootID:       opts.RootID,
		rootPath:     opts.RootPath,
		cookies:      opts.Cookie,
		limitRate:    opts.LimitRate,
		metrics:      util.NewBuffer(500),
		cookieSource: "config",
	}
}

func (d *Driver) Init(ctx context.Context) error {
	d.loadCookieState()
	if d.cookies == "" {
		return fmt.Errorf("115: Init: missing cookie")
	}
	if d.limitRate > 0 {
		d.limiter = rate.NewLimiter(rate.Limit(d.limitRate), 1)
	}
	d.cl = driver115.New(
		driver115.UA(fmt.Sprintf("Mozilla/5.0 115Browser/%s", appVer)),
	)
	d.cl.Client.OnAfterResponse(func(_ *resty.Client, _ *resty.Response) error {
		d.saveCurrentCookiesFromClient()
		return nil
	})
	cred := &driver115.Credential{}
	if err := cred.FromCookie(d.cookies); err != nil {
		d.setLastError(fmt.Sprintf("115: parse cookie: %v", err))
		return fmt.Errorf("115: parse cookie: %w", err)
	}
	d.cl.ImportCredential(cred)
	if err := d.recordSDK(ctx, "login_check", nil, func() error {
		return d.loginCheckWithRetry(ctx, d.cl.LoginCheck)
	}); err != nil {
		d.setLastError(fmt.Sprintf("115: login check: %v", err))
		return fmt.Errorf("115: login check: %w", err)
	}
	d.saveCookieState(d.currentCookieHeader(), d.cookieSource)
	d.httpClient = d.cl.Client.GetClient()
	if d.rootID == "" {
		d.rootID = "0"
	}
	if d.rootPath != "" && d.rootPath != "/" {
		rootID, err := d.resolvePathFrom(ctx, d.rootID, d.rootPath)
		if err != nil {
			return fmt.Errorf("115: resolve root_path %q: %w", d.rootPath, err)
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
	var entries []drive.Entry
	err := d.recordSDK(ctx, "list", map[string]any{"parent_id": d.resolveID(parentID)}, func() error {
		var err error
		entries, err = d.getFiles(d.resolveID(parentID))
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: list %q: %v", parentID, err))
		return nil, err
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, e drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("115: invalid offset or size")
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
		d.setLastError(fmt.Sprintf("115: read pick_code %q: %v", e.ID, err))
		return nil, err
	}
	var info *driver115.DownloadInfo
	err = d.recordSDK(ctx, "download_info", map[string]any{"id": e.ID, "name": e.Name, "offset": offset, "size": size}, func() error {
		var err error
		info, err = d.cl.DownloadWithUA(pickCode, d.userAgent())
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: download info %q: %v", e.ID, err))
		return nil, fmt.Errorf("115: download info: %w", err)
	}
	if info == nil || info.Url.Url == "" {
		return nil, fmt.Errorf("115: download info missing url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.Url.Url, nil)
	if err != nil {
		return nil, err
	}
	req.Header = info.Header.Clone()
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", d.userAgent())
	}
	if size > 0 {
		end := offset + size - 1
		if rawSize > 0 && end >= rawSize {
			end = rawSize - 1
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	} else if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	client := d.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		d.metrics.Record(ctx, drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       util.URL(req.URL),
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"id": e.ID, "offset": offset, "size": size, "range": req.Header.Get("Range")},
			Error:     err.Error(),
		})
		d.setLastError(fmt.Sprintf("115: read %q: %v", e.ID, err))
		return nil, fmt.Errorf("115: read: %w", err)
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
	err = fmt.Errorf("115: read: %s body=%q", resp.Status, util.Snippet(raw))
	d.setLastError(err.Error())
	return nil, err
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.bandwidthLimiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := d.resolveID(req.ParentID), req.Name, req.Source
	if source == nil {
		return drive.Entry{}, drive.NonRetryable(fmt.Errorf("115: upload source is nil"))
	}
	body, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("115: upload source open: %w", err)
	}
	defer body.Close()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	err = d.recordSDK(ctx, "upload", map[string]any{"parent_id": parentID, "name": name, "size": source.Size()}, func() error {
		return d.uploadSource(ctx, parentID, name, source.Size(), body, req.Progress)
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: upload %q: %v", name, err))
		return drive.Entry{}, fmt.Errorf("115: upload: %w", err)
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCompleted)
	entry, err := d.waitUploadedFile(ctx, parentID, name, source)
	if err != nil {
		d.setLastError(err.Error())
		return drive.Entry{}, err
	}
	return entry, nil
}

func (d *Driver) uploadSource(ctx context.Context, parentID, name string, size int64, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	ok, err := d.cl.UploadAvailable()
	if err != nil || !ok {
		return err
	}
	if d.cl.UploadMetaInfo != nil && size > d.cl.UploadMetaInfo.SizeLimit {
		return drive.NonRetryable(driver115.ErrUploadTooLarge)
	}
	digest, err := d.cl.GetDigestResult(body)
	if err != nil {
		return err
	}
	fastInfo, err := d.rapidUpload(size, name, parentID, digest.PreID, digest.QuickID, body)
	if err != nil {
		return err
	}
	instant, err := fastInfo.Ok()
	if err != nil {
		return err
	}
	if instant {
		return nil
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if size < p115MultipartMinSize {
		uploadBody := drive.NewUploadProgressReader(progress, body)
		uploadBody = d.bandwidthLimiter.LimitUpload(ctx, uploadBody)
		return d.cl.UploadByOSS(&fastInfo.UploadOSSParams, uploadBody, parentID)
	}
	return d.uploadMultipart(ctx, parentID, name, size, digest.QuickID, &fastInfo.UploadOSSParams, body, progress)
}

func (d *Driver) rapidUpload(size int64, name, parentID, preID, sha1ID string, body io.ReadSeeker) (*driver115.UploadInitResp, error) {
	ecdhCipher, err := cipher.NewEcdhCipher()
	if err != nil {
		return nil, err
	}
	userID := strconv.FormatInt(d.cl.UserID, 10)
	target := "U_1_" + parentID
	sizeString := strconv.FormatInt(size, 10)
	form := url.Values{}
	form.Set("appid", "0")
	form.Set("appversion", appVer)
	form.Set("userid", userID)
	form.Set("filename", name)
	form.Set("filesize", sizeString)
	form.Set("fileid", sha1ID)
	form.Set("target", target)
	form.Set("sig", d.cl.GenerateSignature(sha1ID, target))

	var result driver115.UploadInitResp
	signKey, signVal := "", ""
	for retry := true; retry; {
		t := driver115.NowMilli()
		encodedToken, err := ecdhCipher.EncodeToken(t.ToInt64())
		if err != nil {
			return nil, err
		}
		form.Set("t", t.String())
		form.Set("token", uploadToken(userID, sha1ID, preID, t.String(), sizeString, signKey, signVal))
		if signKey != "" && signVal != "" {
			form.Set("sign_key", signKey)
			form.Set("sign_val", signVal)
		}
		encrypted, err := ecdhCipher.Encrypt([]byte(form.Encode()))
		if err != nil {
			return nil, err
		}
		req := d.cl.NewRequest().
			SetQueryParam("k_ec", encodedToken).
			SetBody(encrypted).
			SetHeaderVerbatim("Content-Type", "application/x-www-form-urlencoded").
			SetDoNotParseResponse(true)
		resp, err := req.Post(driver115.ApiUploadInit)
		if err != nil {
			return nil, err
		}
		data := resp.RawBody()
		bodyBytes, readErr := io.ReadAll(data)
		closeErr := data.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		decrypted, err := ecdhCipher.Decrypt(bodyBytes)
		if err != nil {
			return nil, err
		}
		result = driver115.UploadInitResp{}
		if err := driver115.CheckErr(json.Unmarshal(decrypted, &result), &result, resp); err != nil {
			return nil, err
		}
		if result.Status != 7 {
			retry = false
			continue
		}
		signKey = result.SignKey
		signVal, err = d.cl.UploadDigestRange(body, result.SignCheck)
		if err != nil {
			return nil, err
		}
	}
	result.SHA1 = sha1ID
	return &result, nil
}

func uploadToken(userID, sha1ID, preID, timestamp, size, signKey, signVal string) string {
	userIDMD5 := md5.Sum([]byte(userID))
	tokenMD5 := md5.Sum([]byte(md5Salt + sha1ID + size + signKey + signVal + userID + timestamp + hex.EncodeToString(userIDMD5[:]) + appVer))
	return hex.EncodeToString(tokenMD5[:])
}

type p115UploadPartRange struct {
	Number int
	Offset int64
	Size   int64
}

func (d *Driver) uploadMultipart(ctx context.Context, parentID, name string, size int64, sha1Hex string, params *driver115.UploadOSSParams, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	if params == nil || params.Bucket == "" || params.Object == "" {
		return drive.NonRetryable(fmt.Errorf("115: upload oss params missing bucket or object"))
	}
	sessionKey := util.UploadSessionKey(parentID, name, size, strings.ToUpper(sha1Hex))
	session, resumed := d.loadUploadSession(sessionKey)
	if resumed {
		logging.L.InfofEvery("115.upload_resume", time.Second, "[115] upload resume name=%q upload_id=%q completed_parts=%d", name, session.UploadID, len(session.Parts))
		params = session.uploadParams()
	} else {
		session = p115UploadSession{
			Key:       sessionKey,
			ParentID:  parentID,
			Name:      name,
			Size:      size,
			SHA1:      strings.ToUpper(sha1Hex),
			Bucket:    params.Bucket,
			Object:    params.Object,
			PartSize:  p115MultipartPartSize,
			Callback:  params.Callback.Callback,
			CallbackV: params.Callback.CallbackVar,
		}
	}
	partSize := session.PartSize
	if partSize <= 0 {
		partSize = p115MultipartPartSize
		session.PartSize = partSize
	}
	ossToken, err := d.cl.GetOSSToken()
	if err != nil {
		return fmt.Errorf("115: get oss token: %w", err)
	}
	ossClient, err := oss.New(
		d.cl.GetOSSEndpoint(d.cl.UseInternalUpload),
		ossToken.AccessKeyID,
		ossToken.AccessKeySecret,
		oss.EnableMD5(true),
		oss.EnableCRC(true),
	)
	if err != nil {
		return fmt.Errorf("115: create oss client: %w", err)
	}
	bucket, err := ossClient.Bucket(session.Bucket)
	if err != nil {
		return fmt.Errorf("115: open oss bucket: %w", err)
	}
	imur := oss.InitiateMultipartUploadResult{
		Bucket:   session.Bucket,
		Key:      session.Object,
		UploadID: session.UploadID,
	}
	if !resumed || imur.UploadID == "" {
		imur, err = bucket.InitiateMultipartUpload(session.Object,
			oss.SetHeader(driver115.OssSecurityTokenHeaderName, ossToken.SecurityToken),
			oss.UserAgentHeader(driver115.OSSUserAgent),
			oss.EnableSha1(),
			oss.Sequential(),
		)
		if err != nil {
			return fmt.Errorf("115: initiate multipart upload: %w", err)
		}
		session.UploadID = imur.UploadID
	}
	partsByNumber := session.partsByNumber()
	uploadParts := make([]oss.UploadPart, 0, len(p115UploadPartRanges(size, partSize)))
	for _, part := range p115UploadPartRanges(size, partSize) {
		if completed, ok := partsByNumber[part.Number]; ok {
			drive.ReportUploadProgress(progress, part.Size)
			uploadParts = append(uploadParts, completed)
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Until(ossToken.Expiration) < 5*time.Minute {
			ossToken, err = d.cl.GetOSSToken()
			if err != nil {
				return fmt.Errorf("115: refresh oss token: %w", err)
			}
		}
		reader := io.NewSectionReader(body, part.Offset, part.Size)
		uploadBody := drive.NewUploadProgressReader(progress, reader)
		uploadBody = d.bandwidthLimiter.LimitUpload(ctx, uploadBody)
		uploadBody = contextReader{ctx: ctx, reader: uploadBody}
		start := time.Now()
		uploadedPart, err := bucket.UploadPart(imur, uploadBody, part.Size, part.Number, driver115.OssOption(params, ossToken)...)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
		}
		d.metrics.Record(ctx, uploadPartMetric("oss_upload_part", part, start, err))
		if err != nil {
			return d.resumedUploadSessionError(resumed, sessionKey, fmt.Errorf("115: upload part %d: %w", part.Number, err))
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
	_, err = bucket.CompleteMultipartUpload(imur, uploadParts,
		append(driver115.OssOption(params, ossToken), oss.CallbackResult(&bodyBytes))...,
	)
	if err != nil {
		return d.resumedUploadSessionError(resumed, sessionKey, fmt.Errorf("115: complete multipart upload: %w", err))
	}
	var uploadResult driver115.UploadResult
	if err := json.Unmarshal(bodyBytes, &uploadResult); err != nil {
		return d.resumedUploadSessionError(resumed, sessionKey, fmt.Errorf("115: complete multipart response: %w", err))
	}
	if err := uploadResult.Err(string(bodyBytes)); err != nil {
		return d.resumedUploadSessionError(resumed, sessionKey, fmt.Errorf("115: complete multipart result: %w", err))
	}
	d.deleteUploadSession(sessionKey)
	return nil
}

func uploadPartMetric(operation string, part p115UploadPartRange, started time.Time, err error) drive.MetricEvent {
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

func p115UploadPartRanges(size, partSize int64) []p115UploadPartRange {
	if size < 0 || partSize <= 0 {
		return nil
	}
	if size == 0 {
		return []p115UploadPartRange{{Number: 1}}
	}
	parts := make([]p115UploadPartRange, 0, int((size+partSize-1)/partSize))
	for offset, number := int64(0), 1; offset < size; offset, number = offset+partSize, number+1 {
		length := partSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		parts = append(parts, p115UploadPartRange{Number: number, Offset: offset, Size: length})
	}
	return parts
}

func (s p115UploadSession) uploadParams() *driver115.UploadOSSParams {
	params := &driver115.UploadOSSParams{
		SHA1:   s.SHA1,
		Bucket: s.Bucket,
		Object: s.Object,
	}
	params.Callback.Callback = s.Callback
	params.Callback.CallbackVar = s.CallbackV
	return params
}

func (s p115UploadSession) partsByNumber() map[int]oss.UploadPart {
	parts := make(map[int]oss.UploadPart, len(s.Parts))
	for _, part := range s.Parts {
		if part.Number <= 0 || part.ETag == "" {
			continue
		}
		parts[part.Number] = oss.UploadPart{PartNumber: part.Number, ETag: part.ETag}
	}
	return parts
}

func (d *Driver) loadUploadSession(key string) (p115UploadSession, bool) {
	return d.uploadSessionStore().Load(key)
}

func (d *Driver) saveUploadSession(session p115UploadSession) {
	d.uploadSessionStore().Save(session)
}

func (d *Driver) deleteUploadSession(key string) {
	d.uploadSessionStore().Delete(key)
}

func (d *Driver) pruneStoredUploadSessions() {
	d.uploadSessionStore().Prune()
}

func (d *Driver) uploadSessionStore() *util.UploadSessionStore[p115UploadSession] {
	return util.NewUploadSessionStore(util.UploadSessionStoreOptions[p115UploadSession]{
		Store:      d.stateStore,
		File:       p115UploadSessionStateFile,
		MaxAge:     p115UploadSessionMaxAge,
		MaxEntries: p115UploadSessionMaxEntries,
		Key: func(session p115UploadSession) string {
			return session.Key
		},
		Valid: func(key string, session p115UploadSession) bool {
			return session.Key != "" && session.Bucket != "" && session.Object != "" && session.UploadID != "" && session.PartSize > 0 && len(session.Parts) > 0
		},
		UpdatedAt: func(session p115UploadSession) time.Time {
			return session.SavedAt
		},
		Touch: func(session *p115UploadSession, now time.Time) {
			session.SavedAt = now
		},
		OnError: func(err error) {
			logging.L.Warnf("115: upload session state failed: %v", err)
		},
	})
}

func (d *Driver) resumedUploadSessionError(resumed bool, key string, err error) error {
	if resumed && invalidResumedUploadSession(err) {
		d.deleteUploadSession(key)
		return fmt.Errorf("115: resumed upload session invalid, will retry from scratch: %v", err)
	}
	return err
}

func invalidResumedUploadSession(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "nosuchupload") ||
		strings.Contains(s, "invalidupload") ||
		strings.Contains(s, "uploadid") ||
		strings.Contains(s, "upload id") ||
		strings.Contains(s, "404") ||
		strings.Contains(s, "409")
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
	parentID = d.resolveID(parentID)
	var id string
	err := d.recordSDK(ctx, "mkdir", map[string]any{"parent_id": parentID, "name": name}, func() error {
		var err error
		id, err = d.cl.Mkdir(parentID, name)
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: mkdir %q: %v", name, err))
		return drive.Entry{}, err
	}
	entry, err := d.getNewEntry(ctx, id)
	if err == nil {
		return entry, nil
	}
	return drive.Entry{ID: id, ParentID: parentID, Name: name, IsDir: true}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	dstParentID = d.resolveID(dstParentID)
	err := d.recordSDK(ctx, "move", map[string]any{"id": entry.ID, "dst_parent_id": dstParentID}, func() error {
		return d.cl.Move(dstParentID, entry.ID)
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: move %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	err := d.recordSDK(ctx, "rename", map[string]any{"id": entry.ID, "new_name": newName}, func() error {
		return d.cl.Rename(entry.ID, newName)
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: rename %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	err := d.recordSDK(ctx, "remove", map[string]any{"id": entry.ID, "is_dir": entry.IsDir}, func() error {
		return d.removeWithRetry(ctx, entry.ID)
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: remove %q: %v", entry.ID, err))
	}
	return err
}

func (d *Driver) removeWithRetry(ctx context.Context, id string) error {
	var err error
	for attempt := 0; attempt < 7; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		err = d.cl.Delete(id)
		if err == nil || !isPendingDeleteError(err) {
			return err
		}
	}
	return err
}

func isPendingDeleteError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "errno\":990009") || strings.Contains(msg, "操作尚未执行完成")
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	var info driver115.InfoData
	err := d.recordSDK(ctx, "space", nil, func() error {
		var err error
		info, err = d.cl.GetInfo()
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: space: %v", err))
		return drive.Space{}, err
	}
	return drive.Space{
		Total: info.SpaceInfo.AllTotal.Size,
		Free:  info.SpaceInfo.AllRemain.Size,
	}, nil
}

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	return d.resolvePathFrom(ctx, d.rootID, p)
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	d.debugMu.Lock()
	lastError := d.lastError
	d.debugMu.Unlock()
	extra := map[string]any{
		drive.DebugExtraCredentialSource: d.cookieSource,
	}
	if !d.cookieUpdated.IsZero() {
		extra[drive.DebugExtraCredentialUpdated] = d.cookieUpdated
	}
	health := drive.HealthLevelOK
	if lastError != "" {
		health = drive.HealthLevelDegraded
		extra[drive.DebugExtraLastError] = lastError
	}
	return drive.DebugSnapshot{
		Driver:      "115",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			drive.DebugStatRootID:   d.rootID,
			drive.DebugStatRootPath: d.rootPath,
		},
		Extra: extra,
	}, nil
}

func (d *Driver) waitLimit(ctx context.Context) error {
	if d.limiter != nil {
		return d.limiter.Wait(ctx)
	}
	return nil
}

func (d *Driver) getFiles(dirID string) ([]drive.Entry, error) {
	files, err := d.cl.ListWithLimit(dirID, 1000, driver115.WithMultiUrls())
	if err != nil {
		return nil, err
	}
	entries := make([]drive.Entry, len(*files))
	for i, f := range *files {
		entries[i] = drive.Entry{
			ID:       f.GetID(),
			ParentID: f.ParentID,
			Name:     f.GetName(),
			Size:     f.GetSize(),
			IsDir:    f.IsDir(),
			ModTime:  f.ModTime(),
			Extra:    f,
		}
	}
	return entries, nil
}

func (d *Driver) getNewEntry(ctx context.Context, id string) (drive.Entry, error) {
	var f *driver115.File
	err := d.recordSDK(ctx, "get_file", map[string]any{"id": id}, func() error {
		var err error
		f, err = d.cl.GetFile(id)
		return err
	})
	if err != nil {
		return drive.Entry{}, err
	}
	return entryFromFile(*f), nil
}

func entryFromFile(f driver115.File) drive.Entry {
	return drive.Entry{
		ID:       f.GetID(),
		ParentID: f.ParentID,
		Name:     f.GetName(),
		Size:     f.GetSize(),
		IsDir:    f.IsDir(),
		ModTime:  f.ModTime(),
		Extra:    f,
	}
}

func (d *Driver) pickCode(ctx context.Context, entry drive.Entry) (string, error) {
	switch f := entry.Extra.(type) {
	case driver115.File:
		if f.PickCode != "" {
			return f.PickCode, nil
		}
	case *driver115.File:
		if f != nil && f.PickCode != "" {
			return f.PickCode, nil
		}
	}
	var f *driver115.File
	err := d.recordSDK(ctx, "get_file", map[string]any{"id": entry.ID}, func() error {
		var err error
		f, err = d.cl.GetFile(entry.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	if f == nil || f.PickCode == "" {
		return "", fmt.Errorf("115: file %q missing pick_code", entry.ID)
	}
	return f.PickCode, nil
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
	return drive.Entry{}, fmt.Errorf("115: uploaded file %q not visible after upload; files=%v", name, names)
}

func entrySHA1(entry drive.Entry) string {
	switch f := entry.Extra.(type) {
	case driver115.File:
		return strings.ToUpper(f.Sha1)
	case *driver115.File:
		if f != nil {
			return strings.ToUpper(f.Sha1)
		}
	}
	return ""
}

func rawEntrySize(entry drive.Entry) int64 {
	switch f := entry.Extra.(type) {
	case driver115.File:
		return f.GetSize()
	case *driver115.File:
		if f != nil {
			return f.GetSize()
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
			return "", fmt.Errorf("directory %q not found under %q", segment, p)
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

func (d *Driver) loginCheckWithRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		err = fn()
		if err == nil || !isRetryableLoginCheckError(err) || attempt >= len(loginCheckRetryDelays) {
			return err
		}
		timer := time.NewTimer(loginCheckRetryDelays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func isRetryableLoginCheckError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

func (d *Driver) setLastError(value string) {
	d.debugMu.Lock()
	defer d.debugMu.Unlock()
	d.lastError = value
}

func (d *Driver) loadCookieState() {
	if d.stateStore == nil {
		return
	}
	var state cookieState
	err := d.stateStore.LoadJSON("115_cookie.json", &state)
	if err != nil {
		return
	}
	if state.Cookie != "" {
		d.cookies = mergeCookieHeaders(d.cookies, state.Cookie)
		d.cookieSource = "state"
	}
	d.cookieUpdated = state.UpdatedAt
}

func (d *Driver) saveCurrentCookiesFromClient() {
	cookie := d.currentCookieHeader()
	if cookie == "" {
		return
	}
	d.saveUpdatedCookie(cookie)
}

func (d *Driver) currentCookieHeader() string {
	if d.cl == nil || d.cl.Client == nil {
		return d.cookies
	}
	cookie := d.cookies
	restyCookies := d.cl.Client.Cookies
	if len(restyCookies) > 0 {
		cookie = mergeCookieHeaders(cookie, cookieHeader(restyCookies))
	}
	if hc := d.cl.Client.GetClient(); hc != nil && hc.Jar != nil {
		for _, rawURL := range []string{
			"https://115.com/",
			"https://my.115.com/",
			"https://webapi.115.com/",
			"https://proapi.115.com/",
			"https://passportapi.115.com/",
			"https://uplb.115.com/",
		} {
			u, err := url.Parse(rawURL)
			if err != nil {
				continue
			}
			cookie = mergeCookieHeaders(cookie, cookieHeader(hc.Jar.Cookies(u)))
		}
	}
	return cookie
}

func (d *Driver) saveUpdatedCookie(cookie string) {
	if cookie == "" {
		return
	}
	merged := mergeCookieHeaders(d.cookies, cookie)
	if merged == "" || merged == d.cookies {
		return
	}
	d.cookies = merged
	d.cookieSource = "response"
	d.cookieUpdated = time.Now()
	d.saveCookieState(merged, d.cookieSource)
}

func (d *Driver) saveCookieState(cookie, source string) {
	if d.stateStore == nil {
		return
	}
	if cookie == "" {
		return
	}
	if d.cookieUpdated.IsZero() {
		d.cookieUpdated = time.Now()
	}
	d.cookies = cookie
	d.cookieSource = source
	if err := d.stateStore.SaveJSON("115_cookie.json", cookieState{
		Cookie:    cookie,
		UpdatedAt: d.cookieUpdated,
	}); err != nil {
		logging.L.Warnf("[115] save updated cookie state failed: %v", err)
	}
}

func mergeCookieHeaders(base, overlay string) string {
	values := map[string]string{}
	order := []string{}
	for _, cookie := range parseCookieHeader(base) {
		values[cookie.Name] = cookie.Value
		order = append(order, cookie.Name)
	}
	changed := false
	for _, cookie := range parseCookieHeader(overlay) {
		if cookie.Name == "" {
			continue
		}
		if _, ok := values[cookie.Name]; !ok {
			order = append(order, cookie.Name)
		}
		if values[cookie.Name] != cookie.Value {
			values[cookie.Name] = cookie.Value
			changed = true
		}
	}
	if len(order) == 0 {
		return ""
	}
	if !changed {
		return base
	}
	parts := make([]string, 0, len(order))
	seen := map[string]struct{}{}
	for _, name := range order {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; ")
}

func parseCookieHeader(cookieHeader string) []*http.Cookie {
	parts := strings.Split(cookieHeader, ";")
	cookies := make([]*http.Cookie, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok || key == "" {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: key, Value: value})
	}
	return cookies
}

func cookieHeader(cookies []*http.Cookie) string {
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func (d *Driver) userAgent() string {
	return fmt.Sprintf("Mozilla/5.0 115Browser/%s", appVer)
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.BandwidthLimitInstaller = (*Driver)(nil)
var _ drive.StateStoreInstaller = (*Driver)(nil)
