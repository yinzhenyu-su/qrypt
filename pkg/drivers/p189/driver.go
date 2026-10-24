package p189

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/logging"
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
	cl         *client
	rootID     int64
	rootPath   string
	limiter    *drive.BandwidthLimiter
	stateStore drive.StateStore
	// cred owns the shared-session lifecycle for mounts that reference a
	// [[credentials]] entry; no installed session keeps the per-mount state
	// store behaviour.
	cred          *driverutil.CredentialBinding[credentialPayload]
	credOnce      sync.Once
	cookieSource  string
	cookieUpdated time.Time

	// Upload session binding store：只保存 "内容键 → uploadFileId"；
	// p189 无分片进度查询接口，恢复时同句柄全量重传（分片按编号幂等覆盖）。
	sessions       *session.Index
	sessionStoreMu sync.Mutex
	sessionCancel  context.CancelFunc
}

type cookieState struct {
	Cookie                  string    `json:"cookie,omitempty"`
	Username                string    `json:"username,omitempty"`
	UpdatedAt               time.Time `json:"updated_at,omitempty"`
	PasswordReloginFailedAt time.Time `json:"password_relogin_failed_at,omitempty"`
	PasswordReloginError    string    `json:"password_relogin_error,omitempty"`
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

const p189SessionFile = "189_upload_sessions.json"
const p189SessionMaxAge = 24 * time.Hour
const p189SessionExpiryEvery = time.Hour

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
		d.cl.onAuthRecovery = d.recoverCredential
		if rid := params["root_id"]; rid != "" {
			if id, err := strconv.ParseInt(rid, 10, 64); err == nil {
				d.rootID = id
			}
		}
		d.cred = newCredentialBinding(d)
		return d, nil
	})
}

// newCredentialBinding wires the shared-session binding to this driver.
func newCredentialBinding(d *Driver) *driverutil.CredentialBinding[credentialPayload] {
	return driverutil.NewCredentialBinding("189", decodeCredentialPayload, d.applyCredentialBinding)
}

// credentialBinding returns the shared-session binding, creating it for
// drivers built without the registered constructor (tests construct Driver
// literals).
func (d *Driver) credentialBinding() *driverutil.CredentialBinding[credentialPayload] {
	d.credOnce.Do(func() {
		if d.cred == nil {
			d.cred = newCredentialBinding(d)
		}
	})
	return d.cred
}

func (d *Driver) Init(ctx context.Context) error {
	configCookie := d.cl.cookieValue()
	if d.credentialBinding().Session() != nil {
		d.loadCredentialSession()
		d.subscribeCredentialSession()
	} else {
		d.loadCookieState()
	}
	if err := d.loginInitWithFallback(ctx, configCookie, func() error {
		return d.cl.loginInit(ctx)
	}); err != nil {
		return fmt.Errorf("189: login init: %w", err)
	}
	if cred := d.credentialBinding(); cred.Session() != nil && len(cred.Snapshot().Payload) == 0 {
		if cookie := d.cl.cookieValue(); cookie != "" {
			d.commitCredentialCookie(cookie)
		}
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
	d.credentialBinding().Close()
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

// InstallCredentialSession lets every mount of one configured 189 credential
// share one cookie and password-relogin state.
func (d *Driver) InstallCredentialSession(session drive.CredentialSession) {
	d.credentialBinding().Install(session)
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	id, err := d.resolvePath(ctx, d.rootID, p)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	capacity, err := d.cl.getCapacity(ctx)
	if err != nil {
		return drive.Space{}, err
	}
	return drive.Space{
		Total: capacity.CloudCapacityInfo.TotalSize,
		Free:  capacity.CloudCapacityInfo.FreeSize,
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
	extra := map[string]any{
		drive.DebugExtraCredentialSource:  credentialSource,
		drive.DebugExtraCredentialUpdated: d.cookieUpdated,
	}
	d.credentialBinding().Debug(extra)
	return drive.DebugSnapshot{
		Driver:      "189",
		Health:      drive.HealthLevelOK,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			drive.DebugStatRootID:   strconv.FormatInt(d.rootID, 10),
			drive.DebugStatRootPath: d.rootPath,
		},
		Extra: extra,
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

// loginInitWithFallback runs loginInit with the current cookie and, when the
// persisted state cookie fails while config credentials (cookie or
// username/password) are available, retries with the config credentials alone
// and persists the working cookie. A stale 189_cookie.json therefore cannot
// wedge the mount: only when both the state and the config credentials fail is
// the error returned.

func (d *Driver) loginInitWithFallback(ctx context.Context, configCookie string, login func() error) error {
	err := login()
	if err == nil {
		return nil
	}
	if d.cookieSource != "state" || configCookie == d.cl.cookieValue() {
		return err
	}
	// The state cookie is stale; fall back to the config credentials.
	if configCookie == "" {
		d.cl.clearCookie()
	} else {
		d.cl.setCookie(configCookie)
	}
	d.cookieSource = "config"
	if retryErr := login(); retryErr != nil {
		return retryErr
	}
	// The config cookie authenticated; persist it so later restarts do not
	// reuse the stale state cookie again.
	if cookie := d.cl.cookieValue(); cookie != "" {
		d.saveUpdatedCookie(cookie)
	}
	return nil
}

func (d *Driver) saveUpdatedCookie(cookie string) {
	if cookie == "" {
		return
	}
	if d.credentialBinding().Session() != nil {
		d.commitCredentialCookie(cookie)
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
	if d.credentialBinding().Session() != nil {
		d.commitCredentialState(d.cl.cookieValue(), failedAt, lastError)
		return
	}
	if d.stateStore == nil {
		return
	}
	if err := d.saveState(); err != nil {
		logging.L.Warnf("[189] save password relogin state failed: %v", err)
	}
}

// recoverCredential adopts a newer shared cookie after an auth failure.
func (d *Driver) recoverCredential() bool {
	return d.credentialBinding().Recover()
}

// loadCredentialSession adopts the shared session's cookie and password
// relogin state when this mount starts.
func (d *Driver) loadCredentialSession() {
	d.credentialBinding().Load()
}

// subscribeCredentialSession follows rotations committed by sibling mounts.
func (d *Driver) subscribeCredentialSession() {
	d.credentialBinding().Subscribe()
}

// applyCredentialBinding applies one decoded payload to the client.
func (d *Driver) applyCredentialBinding(value credentialPayload, snapshot drive.CredentialSnapshot, source string) bool {
	if value.Cookie == "" {
		return false
	}
	d.cl.setCookie(value.Cookie)
	d.cl.setPasswordReloginFailure(value.PasswordReloginFailedAt, value.PasswordReloginError)
	d.cookieSource = source
	d.cookieUpdated = snapshot.UpdatedAt
	return true
}

// commitCredentialCookie publishes a rotated cookie together with the current
// password-relogin state.
func (d *Driver) commitCredentialCookie(cookie string) {
	d.cl.authMu.Lock()
	failedAt := d.cl.passwordReloginFailedAt
	lastError := d.cl.passwordReloginError
	d.cl.authMu.Unlock()
	d.commitCredentialState(cookie, failedAt, lastError)
}

func (d *Driver) commitCredentialState(cookie string, failedAt time.Time, lastError string) {
	cred := d.credentialBinding()
	if cred.Session() == nil || cookie == "" {
		return
	}
	payload, err := encodeCredentialPayload(credentialPayload{
		Cookie:                  cookie,
		PasswordReloginFailedAt: failedAt,
		PasswordReloginError:    lastError,
	})
	if err != nil {
		return
	}
	if err := cred.Commit(payload, d.cl.username); err != nil && !errors.Is(err, drive.ErrCredentialVersionConflict) {
		logging.L.Warnf("[189] commit shared credential failed: %v", err)
	}
}

type credentialPayload struct {
	Cookie                  string    `json:"cookie"`
	PasswordReloginFailedAt time.Time `json:"password_relogin_failed_at,omitempty"`
	PasswordReloginError    string    `json:"password_relogin_error,omitempty"`
}

func encodeCredentialPayload(payload credentialPayload) ([]byte, error) {
	return json.Marshal(payload)
}

func decodeCredentialPayload(data []byte) (credentialPayload, error) {
	var payload credentialPayload
	if len(data) == 0 {
		return payload, nil
	}
	err := json.Unmarshal(data, &payload)
	return payload, err
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
		Username:                d.cl.username,
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
