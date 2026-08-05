package p189

import (
	"context"
	"fmt"
	"strconv"
	"time"

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
	configCookie := d.cl.cookieValue()
	d.loadCookieState()
	if err := d.loginInitWithFallback(ctx, configCookie, func() error {
		return d.cl.loginInit(ctx)
	}); err != nil {
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
