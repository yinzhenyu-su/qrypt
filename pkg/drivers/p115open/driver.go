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
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"golang.org/x/time/rate"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/util"
	"github.com/yinzhenyu/qrypt/pkg/drive"
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

func (d *Driver) waitLimit(ctx context.Context) error {
	if d.limiter != nil {
		return d.limiter.Wait(ctx)
	}
	return nil
}
