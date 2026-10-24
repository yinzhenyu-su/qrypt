package quark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/logging"
	"golang.org/x/sync/singleflight"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

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
			WGKeys:   parseWGKeys(params["wg_keys"]),
		}), nil
	})
}

// parseWGKeys parses a "secureNo:hexkey,secureNo:hexkey" override list.
func parseWGKeys(value string) map[int]string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	keys := map[int]string{}
	for _, pair := range strings.Split(value, ",") {
		numberText, key, ok := strings.Cut(strings.TrimSpace(pair), ":")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSpace(numberText))
		if err != nil || strings.TrimSpace(key) == "" {
			continue
		}
		keys[number] = strings.TrimSpace(key)
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}

func New(cookie string, opts Options) *Driver {
	rootID := opts.RootID
	if rootID == "" {
		rootID = "0"
	}
	d := &Driver{
		cl:                newClient(cookie, clientOptions{BaseURL: opts.BaseURL, V2URL: opts.V2URL, WGKeys: opts.WGKeys}),
		cookie:            cookie,
		configCookie:      cookie,
		rootPath:          opts.RootPath,
		rootID:            rootID,
		cookieSource:      "config",
		debugUploads:      map[string]quarkUploadDebug{},
		wgKeys:            opts.WGKeys,
		offlineSessions:   map[string]string{},
		sharePollTimeout:  quarkSharePollTimeout,
		sharePollInterval: quarkSharePollInterval,
		shareSaves:        map[string][]drive.ShareEntry{},
	}
	d.cl.onCookieUpdate = d.saveUpdatedCookie
	d.cl.onAuthRetry = d.recoverCredential
	return d
}

func (d *Driver) Init(ctx context.Context) error {
	configCookie := d.cookie
	if d.credential != nil {
		d.loadCredentialSession()
		d.subscribeCredentialSession()
	} else {
		d.loadCookieState()
	}
	if d.cookie == "" {
		return fmt.Errorf("quark: cookie is required")
	}
	if err := d.validateWithFallback(ctx, configCookie, func() error {
		var resp sortResp
		if err := d.cl.request(ctx, http.MethodGet, "/file/sort", map[string]string{
			"pdir_fid": d.rootID,
			"_size":    "1",
		}, nil, &resp); err != nil {
			return err
		}
		return apiError(resp.respEnvelope)
	}); err != nil {
		return fmt.Errorf("quark: validate cookie: %w", err)
	}
	if d.rootPath != "" && d.rootPath != "/" {
		rootID, err := d.resolvePathFrom(ctx, "0", d.rootPath)
		if err != nil {
			return fmt.Errorf("quark: resolve root_path %q: %w", d.rootPath, err)
		}
		d.rootID = rootID
	}
	// A config cookie that just authenticated replaces whatever rotation state
	// belongs to a previous credential; record it so siblings and later starts
	// reuse the working value.
	if d.cookieSource == "config" {
		if d.credential == nil {
			d.persistCookieState(d.cookie, "config")
		} else if len(d.credential.Snapshot().Payload) == 0 {
			d.commitCredentialCookie(d.cookie)
		}
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error {
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

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

// InstallCredentialSession lets every mount of one configured credential share
// a single rotated cookie.
func (d *Driver) InstallCredentialSession(session drive.CredentialSession) {
	d.credential = session
}

type Driver struct {
	drive.UnsupportedOperations
	cl                 *client
	urlCache           sync.Map
	urlFetch           singleflight.Group
	downloadWarmup     singleflight.Group
	downloadWarmupDone sync.Map
	cookie             string
	// configCookie is the credential from the mount config. It never changes
	// at runtime and seeds the persisted rotation state: state is only reused
	// while this value stays the same.
	configCookie string
	rootPath     string
	rootID       string
	limiter      *drive.BandwidthLimiter
	stateStore   drive.StateStore
	// credential is the shared session installed for mounts that reference a
	// [[credentials]] entry. When nil the driver keeps its per-mount state
	// store behaviour (legacy inline credentials).
	credential         drive.CredentialSession
	credentialCancel   func()
	credentialMu       sync.Mutex
	credentialVersion  uint64
	cookieSource       string
	cookieUpdated      time.Time
	debugMu            sync.Mutex
	debugUploads       map[string]quarkUploadDebug
	lastError          string
	instantUploadCount int64

	// Upload session binding store：只保存 "内容键 → provider 上传句柄"；
	// quark 无分片进度查询接口，恢复时同句柄全量重传（分片按编号幂等覆盖）。
	sessions       *session.Index
	sessionStoreMu sync.Mutex
	sessionCancel  context.CancelFunc

	// Offline adapter state. wgKeys overrides the built-in wg data-key table;
	// offlineSessions holds accelerated download sessions keyed by provider
	// task id (in-memory: a lost session degrades to the normal URL).
	// offlineLayouts remembers, per task id, the entry the task produced, its
	// staging parent, and whether the staged results are already complete. Core
	// re-seeds it from persisted task metadata after a restart.
	wgKeys          map[int]string
	offlineMu       sync.Mutex
	offlineSessions map[string]string
	offlineLayouts  map[string]drive.OfflineLayout

	// sharePollTimeout bounds synchronous share-link tasks. Tests shorten it
	// to exercise the timeout branch without waiting the production window.
	sharePollTimeout  time.Duration
	sharePollInterval time.Duration

	// shareSaveMu guards shareSaves: the selected entries of an asynchronous
	// provider save, keyed by task id. The task response reports only successful
	// FIDs, so without the request entries a partial save would look complete.
	shareSaveMu sync.Mutex
	shareSaves  map[string][]drive.ShareEntry
}

type Options struct {
	RootPath string
	RootID   string
	BaseURL  string
	V2URL    string
	// WGKeys overrides the built-in wg data-key table for offline request
	// encryption (secureNo → 16-byte hex key).
	WGKeys map[int]string
}

type cookieState struct {
	Cookie string `json:"cookie,omitempty"`
	// Seed fingerprints the config cookie this state was derived from. A
	// rotated session is only reused while the configured credential is
	// unchanged; replacing the cookie (switching accounts, for example) must
	// not stay masked by a session persisted for the previous credential.
	Seed string `json:"seed,omitempty"`
	// Account is the provider account identity (__uid) carried by Cookie. It
	// lets state files written before Seed existed be checked as well and
	// makes the owning account visible in the state file.
	Account   string    `json:"account,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

const quarkSessionFile = "quark_upload_sessions.json"

const quarkSessionMaxAge = 24 * time.Hour

const quarkSessionExpiryEvery = time.Hour

const quarkDownloadMaxRetries = 3

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
		Extra: d.credentialDebugExtra(),
	}, nil
}

func (d *Driver) credentialDebugExtra() map[string]any {
	extra := map[string]any{
		drive.DebugExtraCredentialSource:   d.cookieSource,
		drive.DebugExtraCredentialUpdated:  d.cookieUpdated,
		drive.DebugExtraLastError:          d.getLastError(),
		drive.DebugExtraInstantUploadCount: d.instantUploadCount,
	}
	if d.credential != nil {
		snapshot := d.credential.Snapshot()
		extra[drive.DebugExtraCredentialName] = d.credential.Name()
		extra[drive.DebugExtraCredentialVersion] = snapshot.Version
		extra[drive.DebugExtraCredentialAccount] = snapshot.Account
	}
	return extra
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.cl.metricEvents(since), nil
}

func (d *Driver) getLastError() string {
	d.debugMu.Lock()
	defer d.debugMu.Unlock()
	return d.lastError
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	var resp struct {
		respEnvelope
		Data struct {
			Total int64 `json:"total_capacity"`
			Used  int64 `json:"use_capacity"`
		} `json:"data"`
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
	if err := apiError(resp.respEnvelope); err != nil {
		return drive.Space{}, fmt.Errorf("quark: space: %w", err)
	}
	return drive.Space{
		Total: resp.Data.Total,
		Free:  resp.Data.Total - resp.Data.Used,
	}, nil
}

func (d *Driver) loadCookieState() {
	if d.stateStore == nil {
		return
	}
	var state cookieState
	if err := d.stateStore.LoadJSON("quark_cookie.json", &state); err != nil {
		return
	}
	d.cookieUpdated = state.UpdatedAt
	if state.Cookie == "" || !d.adoptableCookieState(state) {
		return
	}
	d.cookie = state.Cookie
	d.cl.setCookie(state.Cookie)
	d.cookieSource = "state"
}

// adoptableCookieState reports whether persisted rotation state may replace the
// configured cookie. Rotated sessions have to survive restarts for the same
// credential, but a credential the user replaced — a different account in
// particular — always wins over a session persisted earlier.
func (d *Driver) adoptableCookieState(state cookieState) bool {
	if d.configCookie == "" {
		// No configured credential to protect: the persisted session is the
		// only credential available.
		return true
	}
	if state.Seed != "" {
		return state.Seed == cookieFingerprint(d.configCookie)
	}
	// State written before Seed existed: compare the account identity when
	// both sides expose one. Without an identity the session is kept, which
	// matches the behaviour of those versions.
	configAccount := cookieAccount(d.configCookie)
	stateAccount := cookieAccount(state.Cookie)
	if configAccount == "" || stateAccount == "" {
		return true
	}
	return configAccount == stateAccount
}

// validateWithFallback runs validate with the current cookie and, when the
// persisted state cookie fails while a config cookie is available, retries
// with the config cookie alone and replaces the stale quark_cookie.json.
// A stale state file therefore cannot wedge the mount: only when both the
// state and the config cookie fail is the error returned.

func (d *Driver) validateWithFallback(ctx context.Context, configCookie string, validate func() error) error {
	err := validate()
	if err == nil {
		return nil
	}
	if d.cookieSource != "state" || configCookie == "" || configCookie == d.cookie {
		return err
	}
	// The state cookie is stale; fall back to the config cookie.
	d.cookie = configCookie
	d.cl.setCookie(configCookie)
	d.cookieSource = "config"
	if retryErr := validate(); retryErr != nil {
		return retryErr
	}
	// The config cookie authenticated; record it so later restarts and sibling
	// mounts do not reuse the stale session again.
	d.rememberCookie(d.cookie, "config")
	return nil
}

// rememberCookie persists the active cookie either in the shared credential
// session or, for legacy inline mounts, in the per-mount state file.
func (d *Driver) rememberCookie(cookie, source string) {
	if d.credential != nil {
		d.commitCredentialCookie(cookie)
		return
	}
	d.persistCookieState(cookie, source)
}

// loadCredentialSession adopts the shared session's cookie when it belongs to
// the configured account.
func (d *Driver) loadCredentialSession() {
	snapshot := d.credential.Snapshot()
	cookie := decodeCredentialCookie(snapshot.Payload)
	if cookie == "" {
		return
	}
	if config := cookieAccount(d.configCookie); config != "" {
		if session := cookieAccount(cookie); session != "" && session != config {
			return
		}
	}
	d.cookie = cookie
	d.cl.setCookie(cookie)
	d.cookieSource = "state"
	d.cookieUpdated = snapshot.UpdatedAt
	d.credentialVersion = snapshot.Version
}

// subscribeCredentialSession follows rotations committed by sibling mounts.
func (d *Driver) subscribeCredentialSession() {
	if d.credential == nil || d.credentialCancel != nil {
		return
	}
	d.credentialCancel = d.credential.Subscribe(d.applyCredentialSnapshot)
}

// applyCredentialSnapshot moves local state to a newer shared session. Older
// snapshots are ignored so an out-of-order notification cannot undo a rotation.
func (d *Driver) applyCredentialSnapshot(snapshot drive.CredentialSnapshot) {
	cookie := decodeCredentialCookie(snapshot.Payload)
	if cookie == "" {
		return
	}
	d.credentialMu.Lock()
	if snapshot.Version <= d.credentialVersion {
		d.credentialMu.Unlock()
		return
	}
	d.credentialVersion = snapshot.Version
	d.cookie = cookie
	d.cookieSource = "credential"
	d.cookieUpdated = snapshot.UpdatedAt
	d.credentialMu.Unlock()
	d.cl.setCookie(cookie)
}

// commitCredentialCookie publishes a rotated cookie to every mount sharing the
// credential. A version conflict means a sibling committed first, so adopt its
// value instead of overwriting it.
// recoverCredential adopts a session a sibling mount rotated after this mount's
// last request failed with 401.
func (d *Driver) recoverCredential() bool {
	if d.credential == nil {
		return false
	}
	d.credentialMu.Lock()
	version := d.credentialVersion
	d.credentialMu.Unlock()
	next, adopted, err := drive.AdoptLatestCredential(d.credential, version, func(payload []byte) error {
		if cookie := decodeCredentialCookie(payload); cookie != "" {
			d.cl.setCookie(cookie)
		}
		return nil
	})
	if err != nil || !adopted {
		return false
	}
	d.credentialMu.Lock()
	if next > d.credentialVersion {
		d.credentialVersion = next
		d.cookieSource = "credential"
	}
	d.credentialMu.Unlock()
	return true
}

func (d *Driver) commitCredentialCookie(cookie string) {
	if d.credential == nil || cookie == "" {
		return
	}
	snapshot := d.credential.Snapshot()
	_, err := d.credential.Update(snapshot.Version, encodeCredentialCookie(cookie), cookieAccount(cookie))
	if err == nil {
		return
	}
	if errors.Is(err, drive.ErrCredentialVersionConflict) {
		d.applyCredentialSnapshot(d.credential.Snapshot())
		return
	}
	logging.L.Warnf("[QUARK] commit shared credential failed: %v", err)
}

type credentialPayload struct {
	Cookie string `json:"cookie"`
}

func encodeCredentialCookie(cookie string) []byte {
	data, err := json.Marshal(credentialPayload{Cookie: cookie})
	if err != nil {
		return nil
	}
	return data
}

func decodeCredentialCookie(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var decoded credentialPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return ""
	}
	return decoded.Cookie
}

// cookieFingerprint returns a stable digest of a cookie header. Pairs are
// sorted so a re-exported cookie whose keys were reordered still matches.
func cookieFingerprint(cookie string) string {
	pairs := make([]string, 0, 16)
	for pair := range strings.SplitSeq(cookie, ";") {
		if pair = strings.TrimSpace(pair); pair != "" {
			pairs = append(pairs, pair)
		}
	}
	if len(pairs) == 0 {
		return ""
	}
	slices.Sort(pairs)
	sum := sha256.Sum256([]byte(strings.Join(pairs, "; ")))
	return hex.EncodeToString(sum[:])
}

// cookieAccount returns the provider account identity (__uid) in a cookie.
func cookieAccount(cookie string) string {
	for pair := range strings.SplitSeq(cookie, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.TrimSpace(key) == "__uid" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (d *Driver) saveUpdatedCookie(cookie string) {
	if d.credential != nil {
		d.commitCredentialCookie(cookie)
		return
	}
	d.persistCookieState(cookie, "response")
}

// persistCookieState records the active cookie together with the config cookie
// it belongs to, so a later restart can tell a rotated session apart from a
// credential the user replaced.
func (d *Driver) persistCookieState(cookie, source string) {
	if cookie == "" {
		return
	}
	d.cookie = cookie
	d.cookieSource = source
	d.cookieUpdated = time.Now()
	if d.stateStore == nil {
		return
	}
	if err := d.stateStore.SaveJSON("quark_cookie.json", cookieState{
		Cookie:    cookie,
		Seed:      cookieFingerprint(d.configCookie),
		Account:   cookieAccount(cookie),
		UpdatedAt: d.cookieUpdated,
	}); err != nil {
		logging.L.Warnf("[QUARK] save updated cookie state failed: %v", err)
	}
}

func (d *Driver) resolve(id string) string {
	if id == "" || id == "0" || id == "/" {
		return d.rootID
	}
	return id
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func quarkDurationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	ms := d.Milliseconds()
	if ms == 0 {
		return 1
	}
	return ms
}

var _ drive.BandwidthLimitInstaller = (*Driver)(nil)

var _ drive.BandwidthLimitInstaller = (*Driver)(nil)
