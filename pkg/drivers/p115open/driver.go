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
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"golang.org/x/time/rate"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/p115offline"
	"github.com/yinzhenyu/qrypt/pkg/logging"
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
	metrics            *driverutil.Buffer
	stateStore         drive.StateStore
	accessToken        string
	refreshToken       string
	configRefreshToken string
	tokenSource        string
	tokenUpdated       time.Time
	tokenMu            sync.Mutex
	// credential is the shared session installed for mounts that reference a
	// [[credentials]] entry; nil keeps the legacy per-mount token state.
	credential        drive.CredentialSession
	credentialCancel  func()
	credentialMu      sync.Mutex
	credentialVersion uint64
	suppressCommit    bool
	tokenExpires      time.Time
	userID            int64
	debugMu           sync.Mutex
	lastError         string
	instantUploads    atomic.Int64
	bandwidthLimiter  *drive.BandwidthLimiter
	ossMu             sync.Mutex
	ossToken          *sdk.UploadGetTokenResp
	ossTokenAt        time.Time

	// Upload session binding store：只保存 "内容键 → OSS 上传句柄"，
	// 分片进度用服务端 ListParts 重建。
	sessions       *session.Index
	sessionStoreMu sync.Mutex
	sessionCancel  context.CancelFunc

	offlineOnce    sync.Once
	offlineAdapter *p115offline.Adapter
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
	})
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
		metrics:            driverutil.NewBuffer(500),
	}
}

func (d *Driver) Init(ctx context.Context) error {
	if d.credential != nil {
		d.loadCredentialSession()
		d.subscribeCredentialSession()
	} else {
		d.loadTokenState()
	}
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
	if d.credential != nil && d.needsRefresh(time.Now()) {
		if err := d.refreshShared(ctx); err != nil {
			d.setLastError(fmt.Sprintf("115_open: refresh token: %v", err))
			return fmt.Errorf("115_open: refresh token: %w", err)
		}
	}
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
	if d.credential == nil {
		d.saveTokens(d.accessToken, d.refreshToken, d.tokenSource)
	} else if len(d.credential.Snapshot().Payload) == 0 {
		d.commitTokens(d.accessToken, d.refreshToken)
	}
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
	if d.credentialCancel != nil {
		d.credentialCancel()
		d.credentialCancel = nil
	}
	d.sessionStoreMu.Lock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessionStoreMu.Unlock()
	if d.sessions != nil {
		_ = d.sessions.Flush()
	}
	return nil
}

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
	d.installSessionIndex(store)
}

// InstallCredentialSession lets every mount of one configured 115-open
// credential share one token pair and coalesce refreshes.
func (d *Driver) InstallCredentialSession(session drive.CredentialSession) {
	d.credential = session
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.bandwidthLimiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.metrics.Events(since), nil
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
	driverutil.AppendCredentialDebug(extra, d.credential)
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

func (d *Driver) recordSDK(ctx context.Context, operation string, request map[string]any, fn func() error) error {
	start := time.Now()
	err := d.ensureToken(ctx)
	if err == nil {
		err = fn()
	}
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
	if d.credential != nil {
		d.credentialMu.Lock()
		suppressed := d.suppressCommit
		d.credentialMu.Unlock()
		if !suppressed {
			d.commitTokens(accessToken, refreshToken)
		}
		return
	}
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

const tokenRefreshSkew = 5 * time.Minute

// needsRefresh reports whether the shared access token is missing or close to
// expiry.
func (d *Driver) needsRefresh(now time.Time) bool {
	d.tokenMu.Lock()
	accessToken, expires := d.accessToken, d.tokenExpires
	d.tokenMu.Unlock()
	if accessToken == "" {
		return true
	}
	if expires.IsZero() {
		return false
	}
	return now.After(expires.Add(-tokenRefreshSkew))
}

func (d *Driver) ensureToken(ctx context.Context) error {
	if d.credential == nil || !d.needsRefresh(time.Now()) {
		return nil
	}
	return d.refreshShared(ctx)
}

// refreshShared coalesces refreshes across every mount of the credential.
func (d *Driver) refreshShared(ctx context.Context) error {
	snapshot, err := d.credential.Refresh(ctx, func(current drive.CredentialSnapshot) ([]byte, string, error) {
		payload, decodeErr := decodeCredentialPayload(current.Payload)
		if decodeErr != nil && len(current.Payload) > 0 {
			return nil, "", decodeErr
		}
		if payload.RefreshToken != "" {
			d.cl.SetRefreshToken(payload.RefreshToken)
			d.tokenMu.Lock()
			d.refreshToken = payload.RefreshToken
			d.tokenMu.Unlock()
		}
		if payload.AccessToken != "" {
			d.cl.SetAccessToken(payload.AccessToken)
			d.tokenMu.Lock()
			d.accessToken = payload.AccessToken
			d.tokenMu.Unlock()
		}
		d.credentialMu.Lock()
		d.suppressCommit = true
		d.credentialMu.Unlock()
		resp, refreshErr := d.cl.RefreshToken(ctx)
		d.credentialMu.Lock()
		d.suppressCommit = false
		d.credentialMu.Unlock()
		if refreshErr != nil {
			return nil, "", refreshErr
		}
		expires := time.Now().Add(time.Hour)
		if resp.ExpiresIn > 0 {
			expires = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
		}
		d.tokenMu.Lock()
		d.accessToken = resp.AccessToken
		d.refreshToken = resp.RefreshToken
		d.tokenExpires = expires
		d.tokenMu.Unlock()
		return encodeCredentialPayload(credentialPayload{
			AccessToken:  resp.AccessToken,
			RefreshToken: resp.RefreshToken,
			ExpiresAt:    expires,
		})
	})
	if err != nil {
		return err
	}
	d.applyCredentialSnapshot(snapshot)
	return nil
}

func (d *Driver) commitTokens(accessToken, refreshToken string) {
	if d.credential == nil || accessToken == "" || refreshToken == "" {
		return
	}
	d.tokenMu.Lock()
	expires := d.tokenExpires
	d.tokenMu.Unlock()
	payload, _, err := encodeCredentialPayload(credentialPayload{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expires,
	})
	if err != nil {
		return
	}
	snapshot := d.credential.Snapshot()
	_, err = d.credential.Update(snapshot.Version, payload, "")
	if err == nil {
		return
	}
	if errors.Is(err, drive.ErrCredentialVersionConflict) {
		d.applyCredentialSnapshot(d.credential.Snapshot())
		return
	}
	logging.L.Warnf("[115_open] commit shared credential failed: %v", err)
}

func (d *Driver) loadCredentialSession() {
	snapshot := d.credential.Snapshot()
	payload, err := decodeCredentialPayload(snapshot.Payload)
	if err != nil {
		logging.L.Warnf("[115_open] decode shared credential session: %v", err)
		return
	}
	if payload.AccessToken == "" && payload.RefreshToken == "" {
		return
	}
	d.accessToken = payload.AccessToken
	d.refreshToken = payload.RefreshToken
	d.tokenExpires = payload.ExpiresAt
	d.tokenSource = "state"
	d.tokenUpdated = snapshot.UpdatedAt
	d.credentialVersion = snapshot.Version
}

func (d *Driver) subscribeCredentialSession() {
	if d.credential == nil || d.credentialCancel != nil {
		return
	}
	d.credentialCancel = d.credential.Subscribe(d.applyCredentialSnapshot)
}

func (d *Driver) applyCredentialSnapshot(snapshot drive.CredentialSnapshot) {
	payload, err := decodeCredentialPayload(snapshot.Payload)
	if err != nil {
		logging.L.Warnf("[115_open] decode shared credential notification: %v", err)
		return
	}
	if payload.AccessToken == "" && payload.RefreshToken == "" {
		return
	}
	d.credentialMu.Lock()
	if snapshot.Version <= d.credentialVersion {
		d.credentialMu.Unlock()
		return
	}
	d.credentialVersion = snapshot.Version
	d.tokenMu.Lock()
	d.accessToken = payload.AccessToken
	d.refreshToken = payload.RefreshToken
	d.tokenExpires = payload.ExpiresAt
	d.tokenSource = "credential"
	d.tokenUpdated = snapshot.UpdatedAt
	d.tokenMu.Unlock()
	cl := d.cl
	d.credentialMu.Unlock()
	if cl != nil {
		if payload.AccessToken != "" {
			cl.SetAccessToken(payload.AccessToken)
		}
		if payload.RefreshToken != "" {
			cl.SetRefreshToken(payload.RefreshToken)
		}
	}
}

type credentialPayload struct {
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

func encodeCredentialPayload(payload credentialPayload) ([]byte, string, error) {
	data, err := json.Marshal(payload)
	return data, "", err
}

func decodeCredentialPayload(data []byte) (credentialPayload, error) {
	var payload credentialPayload
	if len(data) == 0 {
		return payload, nil
	}
	err := json.Unmarshal(data, &payload)
	return payload, err
}

func (d *Driver) waitLimit(ctx context.Context) error {
	if d.limiter != nil {
		return d.limiter.Wait(ctx)
	}
	return nil
}
