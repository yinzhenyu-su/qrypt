package aliyundrive

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	defaultRootID         = "root"
	defaultUploadPartSize = 10 << 20
)

type Driver struct {
	drive.UnsupportedOperations
	cl                 *client
	urlCache           sync.Map
	rootID             string
	rootPath           string
	driveID            string
	userID             string
	orderBy            string
	orderDirection     string
	partSize           int64
	limiter            *drive.BandwidthLimiter
	stateStore         drive.StateStore
	tokenSource        string
	tokenUpdated       time.Time
	configRefreshToken string
	debugMu            sync.Mutex
	lastError          string
	instantUploadCount int64
}

type cachedDownloadURL struct {
	URL       string
	ExpiresAt time.Time
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

type aliyunUploadSessionState struct {
	Version  int                            `json:"version"`
	Sessions map[string]aliyunUploadSession `json:"sessions,omitempty"`
}

type aliyunUploadSession struct {
	Key            string           `json:"key"`
	ParentID       string           `json:"parent_id"`
	Name           string           `json:"name"`
	Size           int64            `json:"size"`
	SHA1           string           `json:"sha1"`
	FileID         string           `json:"file_id"`
	UploadID       string           `json:"upload_id"`
	PartSize       int64            `json:"part_size"`
	PartInfoList   []uploadPartInfo `json:"part_info_list,omitempty"`
	CompletedParts map[int]bool     `json:"completed_parts,omitempty"`
	CreatedAt      *time.Time       `json:"created_at,omitempty"`
	UpdatedAt      *time.Time       `json:"updated_at,omitempty"`
	SavedAt        time.Time        `json:"saved_at"`
}

const aliyunUploadSessionStateFile = "aliyundrive_upload_sessions.json"
const aliyunUploadSessionMaxAge = 24 * time.Hour
const aliyunUploadSessionMaxEntries = 1024

func init() {
	drive.Register("aliyundrive", func(params drive.Params) (drive.Driver, error) {
		refreshToken := params["refresh_token"]
		if refreshToken == "" {
			return nil, fmt.Errorf("aliyundrive: missing refresh_token")
		}
		driveID := params["drive_id"]
		if driveID == "" {
			return nil, fmt.Errorf("aliyundrive: missing drive_id")
		}
		return New(Options{
			RefreshToken:   refreshToken,
			DriveID:        driveID,
			RootPath:       params["root_path"],
			APIBaseURL:     params["api_base_url"],
			AuthURL:        params["auth_url"],
			OrderBy:        params["order_by"],
			OrderDirection: params["order_direction"],
		}), nil
	},
		drive.ParamDef{
			Name:        "refresh_token",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "Aliyun Drive refresh token for OAuth authentication",
			Example:     "your-refresh-token",
		},
		drive.ParamDef{
			Name:        "drive_id",
			Type:        "string",
			Required:    true,
			Description: "Aliyun Drive ID",
			Example:     "your-drive-id",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Virtual root path, resolved to the provider folder ID at startup",
			Default:     "/",
			Example:     "/qrypt",
		},
		drive.ParamDef{
			Name:        "api_base_url",
			Type:        "string",
			Description: "Custom API base URL",
			Example:     "https://openapi.alipan.com",
		},
		drive.ParamDef{
			Name:        "auth_url",
			Type:        "string",
			Description: "Custom OAuth token URL",
			Example:     "https://openapi.alipan.com/oauth/authorize",
		},
		drive.ParamDef{
			Name:        "order_by",
			Type:        "string",
			Description: "File listing sort field",
			Example:     "name",
		},
		drive.ParamDef{
			Name:        "order_direction",
			Type:        "string",
			Description: "Sort direction (ASC or DESC)",
			Example:     "ASC",
		},
	)
}

type Options struct {
	RefreshToken   string
	DriveID        string
	RootID         string
	RootPath       string
	APIBaseURL     string
	AuthURL        string
	OrderBy        string
	OrderDirection string
}

func New(opts Options) *Driver {
	rootID := opts.RootID
	if rootID == "" {
		rootID = defaultRootID
	}
	orderBy := opts.OrderBy
	if orderBy == "" {
		orderBy = "updated_at"
	}
	orderDirection := strings.ToUpper(opts.OrderDirection)
	if orderDirection == "" {
		orderDirection = "DESC"
	}
	d := &Driver{
		cl:                 newClient(opts.RefreshToken, clientOptions{APIBaseURL: opts.APIBaseURL, AuthURL: opts.AuthURL}),
		driveID:            opts.DriveID,
		rootID:             rootID,
		rootPath:           opts.RootPath,
		orderBy:            orderBy,
		orderDirection:     orderDirection,
		partSize:           defaultUploadPartSize,
		configRefreshToken: opts.RefreshToken,
		tokenSource:        "config",
	}
	d.cl.onRefresh = d.saveRefreshedToken
	return d
}

func (d *Driver) Init(ctx context.Context) error {
	d.loadTokenState()
	if err := d.cl.refresh(ctx); err != nil {
		return err
	}
	var user userResp
	if err := d.cl.request(ctx, http.MethodPost, "/v2/user/get", map[string]any{}, &user); err != nil {
		return fmt.Errorf("aliyundrive: user get: %w", err)
	}
	d.userID = user.UserID
	if err := d.cl.configureDevice(user.UserID); err != nil {
		return err
	}
	if d.rootPath != "" && d.rootPath != "/" {
		rootID, err := d.ResolvePath(ctx, d.rootPath)
		if err != nil {
			d.setLastError(err)
			return fmt.Errorf("aliyundrive: resolve root_path %q: %w", d.rootPath, err)
		}
		d.rootID = rootID
	}
	if err := d.validateRoot(ctx); err != nil {
		d.setLastError(err)
		return err
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error { return nil }

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
	d.pruneStoredUploadSessions()
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) RequiredUploadHashes() []drive.HashAlgorithm {
	return []drive.HashAlgorithm{drive.HashSHA1}
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	var resp capacityResp
	if err := d.cl.request(ctx, http.MethodPost, "https://api.aliyundrive.com/adrive/v1/user/driveCapacityDetails", map[string]any{}, &resp); err != nil {
		return drive.Space{}, fmt.Errorf("aliyundrive: space: %w", err)
	}
	return drive.Space{Total: resp.DriveTotalSize, Free: resp.DriveTotalSize - resp.DriveUsedSize}, nil
}

// RemoteHash reports the content hash the API returned when the entry was
// listed (aliyundrive computes sha1 of the stored bytes).

func (d *Driver) RemoteHash(_ context.Context, entry drive.Entry) (drive.HashAlgorithm, string, error) {
	raw := drive.RawEntryExtra(entry.Extra)
	f, ok := raw.(file)
	if !ok {
		// The entry carries no file metadata, so no hash is available; the
		// caller degrades on ErrUnsupported without treating it as a network
		// or data error.
		return "", "", drive.ErrUnsupported
	}
	if f.ContentHash == "" {
		return "", "", drive.ErrUnsupported
	}
	return drive.HashSHA1, f.ContentHash, nil
}

func (d *Driver) ResolvePath(ctx context.Context, path string) (string, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return d.rootID, nil
	}
	current := d.rootID
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			continue
		}
		entries, err := d.List(ctx, current)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.Name == segment && entry.IsDir {
				current = entry.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%w: aliyundrive: path not found: %s", drive.ErrNotFound, filepath.Join("/", path))
		}
	}
	return current, nil
}

func (d *Driver) batch(ctx context.Context, srcID, dstID, path string) error {
	var resp batchResp
	body := map[string]any{
		"requests": []map[string]any{{
			"headers": map[string]string{"Content-Type": "application/json"},
			"method":  "POST",
			"id":      srcID,
			"body": map[string]any{
				"drive_id":          d.driveID,
				"file_id":           srcID,
				"to_drive_id":       d.driveID,
				"to_parent_file_id": dstID,
			},
			"url": path,
		}},
		"resource": "file",
	}
	if err := d.cl.request(ctx, http.MethodPost, "/v3/batch", body, &resp); err != nil {
		err = fmt.Errorf("aliyundrive: batch %s drive_id=%q file_id=%q dst_parent_id=%q: %w", path, d.driveID, srcID, dstID, err)
		d.setLastError(err)
		return err
	}
	if len(resp.Responses) == 0 {
		err := fmt.Errorf("aliyundrive: batch %s returned no responses", path)
		d.setLastError(err)
		return err
	}
	item := resp.Responses[0]
	if item.Status >= 200 && item.Status < 300 {
		return nil
	}
	err := fmt.Errorf("aliyundrive: batch %s failed status=%d body=%s", path, item.Status, string(item.Body))
	d.setLastError(err)
	return err
}

func (d *Driver) validateRoot(ctx context.Context) error {
	if d.driveID == "" {
		return fmt.Errorf("aliyundrive: drive_id is required")
	}
	if d.rootID == "" {
		return fmt.Errorf("aliyundrive: root_path resolved to empty folder ID")
	}
	if _, err := d.List(ctx, d.rootID); err != nil {
		return fmt.Errorf("aliyundrive: validate root drive_id=%q root_path=%q resolved_id=%q: %w", d.driveID, d.rootPath, d.rootID, err)
	}
	return nil
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	health := "ok"
	if d.getLastError() != "" {
		health = "degraded"
	}
	return drive.DebugSnapshot{
		Driver:      "aliyundrive",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			"drive_id":              d.driveID,
			drive.DebugStatRootID:   d.rootID,
			drive.DebugStatRootPath: d.rootPath,
			"user_id":               d.userID,
			"order_by":              d.orderBy,
			"order_direction":       d.orderDirection,
		},
		Extra: map[string]any{
			drive.DebugExtraCredentialSource:   d.tokenSource,
			drive.DebugExtraCredentialUpdated:  d.tokenUpdated,
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

func (d *Driver) loadTokenState() {
	if d.stateStore == nil {
		return
	}
	var state tokenState
	err := d.stateStore.LoadJSON("aliyundrive_token.json", &state)
	if err != nil {
		if !drive.IsStateNotExist(err) {
			d.setLastError(fmt.Errorf("aliyundrive: load token state: %w", err))
		}
		return
	}
	// The state wins when it was derived from the current config token (token
	// rotation) or when it predates the source marker. A config token that
	// differs from the token the state was derived from is an account switch.
	stateDerived := state.ConfigRefreshToken == "" || state.ConfigRefreshToken == d.configRefreshToken
	if (state.AccessToken != "" || state.RefreshToken != "") && stateDerived {
		d.cl.setTokens(state.AccessToken, state.RefreshToken)
		d.tokenSource = "state"
	}
	d.tokenUpdated = state.UpdatedAt
}

func (d *Driver) saveRefreshedToken(accessToken, refreshToken string) {
	d.tokenSource = "refresh"
	d.tokenUpdated = time.Now()
	if d.stateStore == nil {
		return
	}
	if err := d.stateStore.SaveJSON("aliyundrive_token.json", tokenState{
		AccessToken:        accessToken,
		RefreshToken:       refreshToken,
		ConfigRefreshToken: d.configRefreshToken,
		UpdatedAt:          d.tokenUpdated,
	}); err != nil {
		d.setLastError(fmt.Errorf("aliyundrive: save token state: %w", err))
	}
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.StateStoreInstaller = (*Driver)(nil)
var _ drive.BandwidthLimitInstaller = (*Driver)(nil)

func (d *Driver) resolveID(fileID string) string {
	if fileID == "" || fileID == "0" || fileID == "/" {
		return d.rootID
	}
	return fileID
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
