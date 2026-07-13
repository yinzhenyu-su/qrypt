// Package p115 implements the 115 cloud drive driver.
package p115

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	cipher "github.com/SheltonZhu/115driver/pkg/crypto/ec115"
	"golang.org/x/time/rate"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/internal/driver/traceutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const defaultAppVer = "35.6.0.3"
const md5Salt = "Qclm8MGWUv59TnrR0XPg"

var appVer = defaultAppVer

type Driver struct {
	cl               *driver115.Pan115Client
	rootID           string
	rootPath         string
	cookies          string
	limitRate        float64
	limiter          *rate.Limiter
	bandwidthLimiter *drive.BandwidthLimiter
	httpClient       *http.Client
	trace            *traceutil.Buffer
	debugMu          sync.Mutex
	lastError        string
}

func init() {
	drive.Register("115", func(params drive.Params) (drive.Driver, error) {
		cookie := params["cookie"]
		if cookie == "" {
			return nil, fmt.Errorf("115: missing cookie")
		}
		return New(Options{
			Cookie:    cookie,
			RootPath:  params["root_path"],
			LimitRate: 2,
		}), nil
	},
		drive.ParamDef{
			Name:        "cookie",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "115 cloud drive authentication cookie",
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
		rootID:    opts.RootID,
		rootPath:  opts.RootPath,
		cookies:   opts.Cookie,
		limitRate: opts.LimitRate,
		trace:     traceutil.NewBuffer(500),
	}
}

func (d *Driver) Init(ctx context.Context) error {
	if d.cookies == "" {
		return fmt.Errorf("115: Init: missing cookie")
	}
	if d.limitRate > 0 {
		d.limiter = rate.NewLimiter(rate.Limit(d.limitRate), 1)
	}
	d.cl = driver115.New(
		driver115.UA(fmt.Sprintf("Mozilla/5.0 115Browser/%s", appVer)),
	)
	cred := &driver115.Credential{}
	if err := cred.FromCookie(d.cookies); err != nil {
		d.setLastError(fmt.Sprintf("115: parse cookie: %v", err))
		return fmt.Errorf("115: parse cookie: %w", err)
	}
	d.cl.ImportCredential(cred)
	if err := d.recordSDK(ctx, "login_check", nil, func() error {
		return d.cl.LoginCheck()
	}); err != nil {
		d.setLastError(fmt.Sprintf("115: login check: %v", err))
		return fmt.Errorf("115: login check: %w", err)
	}
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

func (d *Driver) DebugTrace(ctx context.Context, since time.Time) ([]drive.DebugTraceEvent, error) {
	return d.trace.Events(since), nil
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
		d.trace.Record(ctx, drive.DebugTraceEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       traceutil.URL(req.URL),
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"id": e.ID, "offset": offset, "size": size, "range": req.Header.Get("Range")},
			Error:     err.Error(),
		})
		d.setLastError(fmt.Sprintf("115: read %q: %v", e.ID, err))
		return nil, fmt.Errorf("115: read: %w", err)
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		d.trace.Record(ctx, drive.DebugTraceEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       traceutil.URL(req.URL),
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
	d.trace.Record(ctx, drive.DebugTraceEvent{
		Operation: "download",
		Method:    req.Method,
		URL:       traceutil.URL(req.URL),
		Status:    resp.StatusCode,
		Duration:  time.Since(start).String(),
		Request:   map[string]any{"id": e.ID, "offset": offset, "size": size, "range": req.Header.Get("Range")},
		Response:  map[string]any{"body_snippet": traceutil.Snippet(raw)},
	})
	err = fmt.Errorf("115: read: %s body=%q", resp.Status, traceutil.Snippet(raw))
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
		return drive.Entry{}, fmt.Errorf("115: upload source is nil")
	}
	body, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("115: upload source open: %w", err)
	}
	defer body.Close()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	uploadBody := drive.NewUploadProgressReader(req.Progress, body)
	uploadBody = d.bandwidthLimiter.LimitUpload(ctx, uploadBody)
	uploadSeekBody, ok := uploadBody.(io.ReadSeeker)
	if !ok {
		return drive.Entry{}, fmt.Errorf("115: upload source is not seekable after wrapping")
	}
	err = d.recordSDK(ctx, "upload", map[string]any{"parent_id": parentID, "name": name, "size": source.Size()}, func() error {
		return d.uploadSource(parentID, name, source.Size(), uploadSeekBody)
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

func (d *Driver) uploadSource(parentID, name string, size int64, body io.ReadSeeker) error {
	ok, err := d.cl.UploadAvailable()
	if err != nil || !ok {
		return err
	}
	if d.cl.UploadMetaInfo != nil && size > d.cl.UploadMetaInfo.SizeLimit {
		return driver115.ErrUploadTooLarge
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
	return d.cl.UploadByOSS(&fastInfo.UploadOSSParams, body, parentID)
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
		drive.DebugExtraCredentialSource: "cookie",
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
	event := drive.DebugTraceEvent{
		Layer:     "driver.sdk",
		Operation: operation,
		Duration:  time.Since(start).String(),
		Request:   request,
	}
	if err != nil {
		event.Error = err.Error()
	}
	d.trace.Record(ctx, event)
	return err
}

func (d *Driver) setLastError(value string) {
	d.debugMu.Lock()
	defer d.debugMu.Unlock()
	d.lastError = value
}

func (d *Driver) userAgent() string {
	return fmt.Sprintf("Mozilla/5.0 115Browser/%s", appVer)
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.Writer = (*Driver)(nil)
var _ drive.SourceUploader = (*Driver)(nil)
var _ drive.BandwidthLimitInstaller = (*Driver)(nil)
var _ drive.SpaceQuerier = (*Driver)(nil)
var _ drive.PathResolver = (*Driver)(nil)
var _ drive.Debugger = (*Driver)(nil)
var _ drive.DebugTraceProvider = (*Driver)(nil)
