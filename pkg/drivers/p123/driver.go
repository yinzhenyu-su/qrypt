package p123

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

const (
	tokenStateFile = "123_token.json"

	// defaultRootID is 123pan's account root ("我的文件").
	defaultRootID = "0"

	downloadCacheTTL = 10 * time.Minute
	// downloadURLCacheMax caps the in-memory resolved-URL cache so a long-lived
	// mount over a huge library cannot grow without bound.
	downloadURLCacheMax = 512
)

// Options configures a 123pan driver instance.
type Options struct {
	Username string
	Password string
	RootPath string
	RootID   string
	BaseURL  string
}

type Driver struct {
	drive.UnsupportedOperations

	cl       *client
	rootID   string
	rootPath string
	limiter  *drive.BandwidthLimiter

	stateStore   drive.StateStore
	tokenStateMu sync.Mutex
	tokenSource  string
	tokenUpdated time.Time

	// credential is the shared session installed for mounts that reference a
	// [[credentials]] entry; nil keeps the legacy per-mount token state.
	credential        drive.CredentialSession
	credentialCancel  func()
	credentialMu      sync.Mutex
	credentialVersion uint64
	suppressCommit    bool

	// Upload session bindings: content key → provider upload handle, used to
	// resume interrupted multipart uploads.
	sessions       *session.Index
	sessionStoreMu sync.Mutex
	sessionCancel  context.CancelFunc

	urlMu    sync.Mutex
	urlCache map[string]cachedDownload

	lastErrMu      sync.Mutex
	lastErr        string
	instantUploads atomic.Int64
}

type tokenState struct {
	Token     string    `json:"token,omitempty"`
	Username  string    `json:"username,omitempty"`
	Expire    time.Time `json:"expire,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

func init() {
	drive.Register("123", func(params drive.Params) (drive.Driver, error) {
		username := strings.TrimSpace(params["username"])
		password := params["password"]
		if username == "" || password == "" {
			return nil, fmt.Errorf("123: missing username and password")
		}
		return New(Options{
			Username: username,
			Password: password,
			RootPath: params["root_path"],
			RootID:   params["root_id"],
			BaseURL:  params["base_url"],
		}), nil
	})
}

// New builds a 123pan driver. Credentials are validated on Init.
func New(opts Options) *Driver {
	d := &Driver{
		cl:          newClient(opts.Username, opts.Password, opts.BaseURL),
		rootID:      strings.TrimSpace(opts.RootID),
		rootPath:    opts.RootPath,
		tokenSource: "config",
		urlCache:    map[string]cachedDownload{},
	}
	d.cl.onTokenUpdate = d.saveToken
	return d
}

func (d *Driver) Init(ctx context.Context) error {
	if d.credential != nil {
		d.loadCredentialSession()
		d.subscribeCredentialSession()
	} else {
		d.loadTokenState()
	}
	if d.cl.tokenValue() == "" {
		login := d.cl.login
		if d.credential != nil {
			login = d.loginShared
		}
		if err := login(ctx); err != nil {
			return err
		}
	}
	if _, err := d.cl.userInfo(ctx); err != nil {
		return fmt.Errorf("123: validate credentials: %w", err)
	}
	// An explicit root_id wins over root_path: the operator already resolved
	// the directory, so re-walking root_path (and doing it from the default
	// root, not from root_id) would mount the wrong tree or fail outright.
	// Commit the root only after the resolution succeeds, so a retried Init
	// re-runs it instead of quietly falling back to the account root.
	if d.rootID == "" {
		rootID := defaultRootID
		if d.rootPath != "" && d.rootPath != "/" {
			resolved, err := d.resolvePathFrom(ctx, defaultRootID, d.rootPath)
			if err != nil {
				return fmt.Errorf("123: resolve root_path %q: %w", d.rootPath, err)
			}
			rootID = resolved
		}
		d.rootID = rootID
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

func (d *Driver) resolve(id string) string {
	if id == "" || id == "0" || id == "/" {
		if d.rootID != "" {
			return d.rootID
		}
		return defaultRootID
	}
	return id
}

func (d *Driver) ResolvePath(ctx context.Context, path string) (string, error) {
	return d.resolvePathFrom(ctx, d.rootID, path)
}

func (d *Driver) resolvePathFrom(ctx context.Context, rootID, path string) (string, error) {
	currentID := d.resolve(rootID)
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if segment == "" {
			continue
		}
		entries, err := d.List(ctx, currentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.Name == segment {
				currentID = entry.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%w: 123: child not found: %s", drive.ErrNotFound, segment)
		}
	}
	return currentID, nil
}

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	parentID = d.resolve(parentID)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := d.cl.list(ctx, parentID)
	if err != nil {
		d.setLastError(err)
		return nil, fmt.Errorf("123: list: %w", err)
	}
	entries := make([]drive.Entry, 0, len(files))
	for _, f := range files {
		if f.FileID == 0 {
			continue
		}
		entries = append(entries, f.entry(parentID))
	}
	return entries, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	parent := d.resolve(parentID)
	parentIDInt, err := strconv.ParseInt(parent, 10, 64)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("123: mkdir: invalid parent id %q: %w", parent, drive.ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return drive.Entry{}, err
	}

	// qrypt addresses entries by name, but 123pan keeps files and directories
	// in separate namespaces: it will create "docs/" right beside a file named
	// "docs" and answer a same-name directory with the duplicate code 5060.
	// Resolve the name first so an existing directory is reused and any other
	// kind of collision is rejected instead of committed.
	if existing, found, err := d.lookupChild(ctx, parent, name); err != nil {
		d.setLastError(err)
		return drive.Entry{}, fmt.Errorf("123: mkdir: %w", err)
	} else if found {
		if existing.IsDir {
			return existing, nil
		}
		return drive.Entry{}, mkdirConflictError(name, nil)
	}

	resp, err := d.cl.mkdir(ctx, parentIDInt, name)
	if err != nil {
		if isDuplicate(err) {
			// A concurrent writer created it between the check and the call.
			if existing, found, lookupErr := d.lookupChild(ctx, parent, name); lookupErr == nil && found {
				if existing.IsDir {
					return existing, nil
				}
				return drive.Entry{}, mkdirConflictError(name, nil)
			}
			return drive.Entry{}, mkdirConflictError(name, err)
		}
		d.setLastError(err)
		return drive.Entry{}, fmt.Errorf("123: mkdir: %w", err)
	}
	if resp.Data.Info != nil && resp.Data.Info.FileID != 0 {
		entry := resp.Data.Info.entry(parent)
		if entry.Name != name {
			// The provider renamed the new directory; committing it under the
			// requested path would make the name unaddressable.
			return drive.Entry{}, fmt.Errorf("123: mkdir %q: %w: the provider renamed the new directory to %q",
				name, fs.ErrExist, entry.Name)
		}
		return entry, nil
	}
	entry, err := d.findChild(ctx, parent, name)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("123: mkdir: locate created directory: %w", err)
	}
	// Mkdir must never hand back a non-directory, however the entry appeared.
	if !entry.IsDir {
		return drive.Entry{}, mkdirConflictError(name, nil)
	}
	return entry, nil
}

// mkdirConflictError classifies a name collision so the caller can recover by
// re-listing the parent (drive.ErrorCategoryConflict) instead of treating it
// as a backend failure.
func mkdirConflictError(name string, cause error) error {
	if cause == nil {
		return fmt.Errorf("123: mkdir %q: %w: a non-directory entry with that name already exists", name, fs.ErrExist)
	}
	return fmt.Errorf("123: mkdir %q: %w: %w", name, fs.ErrExist, cause)
}

// lookupChild lists parent once and returns the child named name, reporting
// whether it exists. Unlike findChild it does not retry: a miss is the normal
// case when creating a new entry.
func (d *Driver) lookupChild(ctx context.Context, parent, name string) (drive.Entry, bool, error) {
	entries, err := d.List(ctx, parent)
	if err != nil {
		return drive.Entry{}, false, err
	}
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true, nil
		}
	}
	return drive.Entry{}, false, nil
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) (drive.Entry, error) {
	meta, ok := metaFromEntry(entry)
	if !ok {
		meta = fallbackMeta(entry)
	}
	info, err := d.cl.rename(ctx, meta.FileID, newName)
	if err != nil {
		d.setLastError(err)
		return drive.Entry{}, fmt.Errorf("123: rename: %w", err)
	}
	if info != nil && info.FileID != 0 {
		updated := info.entry(entry.ParentID)
		if updated.Name == "" {
			updated.Name = newName
		}
		return updated, nil
	}
	entry.Name = newName
	if m, ok := metaFromEntry(entry); ok {
		m.Name = newName
		entry.Extra = m
	}
	return entry, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) (drive.Entry, error) {
	meta, ok := metaFromEntry(entry)
	if !ok {
		meta = fallbackMeta(entry)
	}
	dst := d.resolve(dstParentID)
	dstInt, err := strconv.ParseInt(dst, 10, 64)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("123: move: invalid destination id %q: %w", dst, drive.ErrInvalidInput)
	}
	info, err := d.cl.move(ctx, meta.FileID, dstInt)
	if err != nil {
		d.setLastError(err)
		return drive.Entry{}, fmt.Errorf("123: move: %w", err)
	}
	if info != nil && info.FileID != 0 {
		return info.entry(dst), nil
	}
	entry.ParentID = dst
	meta.ParentID = dstInt
	entry.Extra = meta
	return entry, nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	meta, ok := metaFromEntry(entry)
	if !ok {
		meta = fallbackMeta(entry)
	}
	if remoteName, ok := drive.EntryRemoteName(entry); ok && remoteName != "" {
		meta.Name = remoteName
	}
	if err := d.cl.trash(ctx, meta); err != nil {
		d.setLastError(err)
		return fmt.Errorf("123: remove: %w", err)
	}
	d.forgetDownloadURL(entry.ID)
	return nil
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	info, err := d.cl.userInfo(ctx)
	if err != nil {
		return drive.Space{}, fmt.Errorf("123: space: %w", err)
	}
	total := info.Data.SpacePermanent + info.Data.SpaceTemp
	free := total - info.Data.SpaceUsed
	if free < 0 {
		free = 0
	}
	return drive.Space{Total: total, Free: free}, nil
}

// findChild lists parent and returns the entry named name, retrying briefly to
// absorb the eventual consistency between a write and the next listing.
func (d *Driver) findChild(ctx context.Context, parent, name string) (drive.Entry, error) {
	for attempt := 0; ; attempt++ {
		entries, err := d.List(ctx, parent)
		if err != nil {
			return drive.Entry{}, err
		}
		for _, entry := range entries {
			if entry.Name == name {
				return entry, nil
			}
		}
		if attempt >= 2 {
			return drive.Entry{}, fmt.Errorf("%w: 123: child %q not found", drive.ErrNotFound, name)
		}
		select {
		case <-ctx.Done():
			return drive.Entry{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	health := drive.HealthLevelOK
	lastErr := d.getLastError()
	if lastErr != "" {
		health = drive.HealthLevelDegraded
	}
	d.urlMu.Lock()
	urlCount := len(d.urlCache)
	d.urlMu.Unlock()
	d.tokenStateMu.Lock()
	tokenSource := d.tokenSource
	tokenUpdated := d.tokenUpdated
	d.tokenStateMu.Unlock()
	extra := map[string]any{
		drive.DebugExtraInstantUploadCount: d.instantUploads.Load(),
		drive.DebugExtraCredentialSource:   tokenSource,
		drive.DebugExtraCredentialUpdated:  tokenUpdated,
		drive.DebugExtraLastError:          lastErr,
	}
	driverutil.AppendCredentialDebug(extra, d.credential)
	return drive.DebugSnapshot{
		Driver:      "123",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			drive.DebugStatRootID:   d.rootID,
			drive.DebugStatRootPath: d.rootPath,
			"cached_download_urls":  urlCount,
			"token_relogins":        d.cl.reloginCount.Load(),
		},
		Extra: extra,
	}, nil
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.cl.metricEvents(since), nil
}

func (d *Driver) setLastError(err error) {
	if err == nil {
		return
	}
	d.lastErrMu.Lock()
	d.lastErr = err.Error()
	d.lastErrMu.Unlock()
}

func (d *Driver) getLastError() string {
	d.lastErrMu.Lock()
	defer d.lastErrMu.Unlock()
	return d.lastErr
}

func (d *Driver) loadTokenState() {
	if d.stateStore == nil {
		return
	}
	var state tokenState
	if err := d.stateStore.LoadJSON(tokenStateFile, &state); err != nil {
		return
	}
	// Ignore a token minted for a different account or one already past its
	// expiry; Init falls back to a fresh login in either case.
	if state.Token == "" || state.Username != d.cl.username {
		return
	}
	if !state.Expire.IsZero() && time.Now().After(state.Expire) {
		return
	}
	d.cl.setToken(state.Token)
	d.tokenStateMu.Lock()
	d.tokenSource = "state"
	d.tokenUpdated = state.UpdatedAt
	d.tokenStateMu.Unlock()
}

// saveToken persists a freshly obtained token so restarts skip the login call.
func (d *Driver) saveToken(token string, expire time.Time) {
	if token == "" {
		return
	}
	updated := time.Now()
	d.tokenStateMu.Lock()
	d.tokenSource = "login"
	d.tokenUpdated = updated
	d.tokenStateMu.Unlock()
	if d.credential != nil {
		d.credentialMu.Lock()
		suppressed := d.suppressCommit
		d.credentialMu.Unlock()
		if suppressed {
			return
		}
		d.commitCredentialToken(token, expire)
		return
	}
	if d.stateStore == nil {
		return
	}
	if err := d.stateStore.SaveJSON(tokenStateFile, tokenState{
		Token:     token,
		Username:  d.cl.username,
		Expire:    expire,
		UpdatedAt: updated,
	}); err != nil {
		logging.L.Warnf("[123] save token state failed: %v", err)
	}
}

func (d *Driver) loadCredentialSession() {
	snapshot := d.credential.Snapshot()
	payload, err := decodeCredentialPayload(snapshot.Payload)
	if err != nil {
		logging.L.Warnf("[123] decode shared credential session: %v", err)
		return
	}
	if payload.Token == "" || payload.Username != d.cl.username {
		return
	}
	if !payload.Expire.IsZero() && time.Now().After(payload.Expire) {
		return
	}
	d.cl.setToken(payload.Token)
	d.tokenStateMu.Lock()
	d.tokenSource = "state"
	d.tokenUpdated = snapshot.UpdatedAt
	d.tokenStateMu.Unlock()
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
		logging.L.Warnf("[123] decode shared credential notification: %v", err)
		return
	}
	if payload.Token == "" || payload.Username != d.cl.username {
		return
	}
	if !payload.Expire.IsZero() && time.Now().After(payload.Expire) {
		return
	}
	d.credentialMu.Lock()
	if snapshot.Version <= d.credentialVersion {
		d.credentialMu.Unlock()
		return
	}
	d.credentialVersion = snapshot.Version
	d.tokenSource = "credential"
	d.tokenUpdated = snapshot.UpdatedAt
	d.credentialMu.Unlock()
	d.cl.setToken(payload.Token)
}

// loginShared returns an unexpired token from the session or performs one login
// for every mount waiting on the same credential.
func (d *Driver) loginShared(ctx context.Context) error {
	_, err := d.credential.Refresh(ctx, func(current drive.CredentialSnapshot) ([]byte, string, error) {
		payload, decodeErr := decodeCredentialPayload(current.Payload)
		if decodeErr == nil && payload.Token != "" && payload.Username == d.cl.username &&
			(payload.Expire.IsZero() || time.Now().Before(payload.Expire)) {
			d.cl.setToken(payload.Token)
			return current.Payload, d.cl.username, nil
		}
		token, expire, loginErr := d.loginDirect(ctx)
		if loginErr != nil {
			return nil, "", loginErr
		}
		encoded, encodeErr := encodeCredentialPayload(credentialPayload{
			Token:    token,
			Username: d.cl.username,
			Expire:   expire,
		})
		if encodeErr != nil {
			return nil, "", encodeErr
		}
		return encoded, d.cl.username, nil
	})
	return err
}

// loginDirect runs the password login and reports the minted token without
// committing it; loginShared commits the result as one session update.
func (d *Driver) loginDirect(ctx context.Context) (string, time.Time, error) {
	var token string
	var expire time.Time
	previous := d.cl.onTokenUpdate
	d.cl.onTokenUpdate = func(minted string, exp time.Time) {
		token, expire = minted, exp
		if previous != nil {
			previous(minted, exp)
		}
	}
	d.credentialMu.Lock()
	d.suppressCommit = true
	d.credentialMu.Unlock()
	err := d.cl.login(ctx)
	d.credentialMu.Lock()
	d.suppressCommit = false
	d.credentialMu.Unlock()
	d.cl.onTokenUpdate = previous
	return token, expire, err
}

func (d *Driver) commitCredentialToken(token string, expire time.Time) {
	if d.credential == nil || token == "" {
		return
	}
	payload, err := encodeCredentialPayload(credentialPayload{
		Token:    token,
		Username: d.cl.username,
		Expire:   expire,
	})
	if err != nil {
		return
	}
	snapshot := d.credential.Snapshot()
	_, err = d.credential.Update(snapshot.Version, payload, d.cl.username)
	if err == nil {
		return
	}
	if errors.Is(err, drive.ErrCredentialVersionConflict) {
		d.applyCredentialSnapshot(d.credential.Snapshot())
		return
	}
	logging.L.Warnf("[123] commit shared credential failed: %v", err)
}

type credentialPayload struct {
	Token    string    `json:"token,omitempty"`
	Username string    `json:"username,omitempty"`
	Expire   time.Time `json:"expire,omitempty"`
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

func errIsNotFound(err error) bool {
	return errors.Is(err, drive.ErrNotFound)
}

func isDuplicate(err error) bool {
	return errors.Is(err, errDuplicate)
}

var _ drive.StateStoreInstaller = (*Driver)(nil)

// InstallStateStore lets the driver persist its bearer token and upload
// session bindings across restarts.
func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
	d.installSessionIndex(store)
}

// InstallCredentialSession lets every mount of one configured 123 credential
// share one token and coalesce logins.
func (d *Driver) InstallCredentialSession(session drive.CredentialSession) {
	d.credential = session
}
