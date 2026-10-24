package p115open

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/OpenListTeam/115-sdk-go"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/p115offline"
)

// p115OpenOfflineAPI is the open-platform surface the offline backend uses.
// AddOfflineTasks is our own wrapper because the SDK's AddOfflineTaskURIs
// drops per-item failures and returns only the successful hashes.
type p115OpenOfflineAPI interface {
	AddOfflineTasks(ctx context.Context, source, dirID string) ([]sdk.AddOfflineTaskURIsResp, error)
	OfflineTaskList(ctx context.Context, page int64) (*sdk.OfflineTaskListResp, error)
	DeleteOfflineTask(ctx context.Context, infoHash string, deleteFiles bool) error
	GetFolderInfo(ctx context.Context, fileID string) (*sdk.GetFolderInfoResp, error)
	DownURL(ctx context.Context, pickCode, userAgent string) (sdk.DownURLResp, error)
}

type p115OpenClient struct {
	cl *sdk.Client
}

func (c p115OpenClient) AddOfflineTasks(ctx context.Context, source, dirID string) ([]sdk.AddOfflineTaskURIsResp, error) {
	var resp []sdk.AddOfflineTaskURIsResp
	_, err := c.cl.AuthRequest(ctx, sdk.ApiAddOffline, http.MethodPost, &resp, sdk.ReqWithForm(sdk.Form{
		"urls":       source,
		"wp_path_id": dirID,
	}))
	return resp, err
}

func (c p115OpenClient) OfflineTaskList(ctx context.Context, page int64) (*sdk.OfflineTaskListResp, error) {
	var resp sdk.OfflineTaskListResp
	_, err := c.cl.AuthRequest(ctx, sdk.ApiOfflineList, http.MethodGet, &resp, sdk.ReqWithQuery(sdk.Form{
		"page": strconv.FormatInt(page, 10),
	}))
	return &resp, err
}

func (c p115OpenClient) DeleteOfflineTask(ctx context.Context, infoHash string, deleteFiles bool) error {
	return c.cl.DeleteOfflineTask(ctx, infoHash, deleteFiles)
}

func (c p115OpenClient) GetFolderInfo(ctx context.Context, fileID string) (*sdk.GetFolderInfoResp, error) {
	return c.cl.GetFolderInfo(ctx, fileID)
}

func (c p115OpenClient) DownURL(ctx context.Context, pickCode, userAgent string) (sdk.DownURLResp, error) {
	return c.cl.DownURL(ctx, pickCode, userAgent)
}

type p115OpenOfflineBackend struct {
	driver *Driver
	api    p115OpenOfflineAPI
}

func (d *Driver) offline() *p115offline.Adapter {
	d.offlineOnce.Do(func() {
		d.offlineAdapter = p115offline.New(p115OpenOfflineBackend{driver: d})
	})
	return d.offlineAdapter
}

func (b p115OpenOfflineBackend) client() p115OpenOfflineAPI {
	if b.api != nil {
		return b.api
	}
	if b.driver != nil && b.driver.cl != nil {
		return p115OpenClient{cl: b.driver.cl}
	}
	return nil
}

func (d *Driver) CreateOfflineDownload(ctx context.Context, req drive.OfflineDownloadRequest) (drive.OfflineDownload, error) {
	return d.offline().CreateOfflineDownload(ctx, req)
}

func (d *Driver) GetOfflineDownload(ctx context.Context, id string) (drive.OfflineDownload, error) {
	return d.offline().GetOfflineDownload(ctx, id)
}

func (d *Driver) CancelOfflineDownload(ctx context.Context, id string) error {
	return d.offline().CancelOfflineDownload(ctx, id)
}

func (d *Driver) OfflineDownloadURL(ctx context.Context, req drive.OfflineFetchRequest) (drive.OfflineDownloadTarget, error) {
	return d.offline().OfflineDownloadURL(ctx, req)
}

func (d *Driver) OfflineSourceKinds() []drive.OfflineSourceKind {
	return d.offline().OfflineSourceKinds()
}

func (d *Driver) RestoreOfflineLayout(layout drive.OfflineLayout) {
	d.offline().RestoreOfflineLayout(layout)
}

func (b p115OpenOfflineBackend) AddTask(ctx context.Context, source, dirID string) (string, error) {
	api := b.client()
	if api == nil {
		return "", drive.NewOfflineError(drive.OfflineErrProvider, "", "115_open: offline API is not initialized")
	}
	if err := b.driver.waitLimit(ctx); err != nil {
		return "", err
	}
	var items []sdk.AddOfflineTaskURIsResp
	err := b.driver.recordSDK(ctx, "offline_add", map[string]any{"dir_id": dirID}, func() error {
		var err error
		items, err = api.AddOfflineTasks(ctx, source, dirID)
		return err
	})
	if err != nil {
		return "", p115OpenOfflineError(err, false)
	}
	if len(items) == 0 {
		return "", drive.NewOfflineError(drive.OfflineErrProvider, "", "115_open: add task returned no result")
	}
	var firstErr error
	for _, item := range items {
		if item.State && strings.TrimSpace(item.InfoHash) != "" {
			return strings.TrimSpace(item.InfoHash), nil
		}
		if firstErr == nil {
			firstErr = p115OpenOfflineCode(item.Code, item.Message)
		}
	}
	if firstErr != nil {
		return "", firstErr
	}
	return "", drive.NewOfflineError(drive.OfflineErrProvider, "", "115_open: add task was rejected")
}

func (b p115OpenOfflineBackend) ListTasks(ctx context.Context, page int) ([]p115offline.Task, int, error) {
	api := b.client()
	if api == nil {
		return nil, 0, drive.NewOfflineError(drive.OfflineErrProvider, "", "115_open: offline API is not initialized")
	}
	if err := b.driver.waitLimit(ctx); err != nil {
		return nil, 0, err
	}
	var resp *sdk.OfflineTaskListResp
	err := b.driver.recordSDK(ctx, "offline_list", map[string]any{"page": page}, func() error {
		var err error
		resp, err = api.OfflineTaskList(ctx, int64(page))
		return err
	})
	if err != nil {
		return nil, 0, p115OpenOfflineError(err, true)
	}
	if resp == nil {
		return nil, 0, drive.NewOfflineError(drive.OfflineErrProviderUnavailable, "", "115_open: empty offline task list response")
	}
	tasks := make([]p115offline.Task, 0, len(resp.Tasks))
	for _, task := range resp.Tasks {
		tasks = append(tasks, p115offline.Task{
			ID:       task.InfoHash,
			Name:     task.Name,
			Size:     task.Size,
			Status:   task.Status,
			Percent:  float64(task.PercentDone),
			FileID:   task.FileID,
			ParentID: task.WpPathID,
			URL:      task.URL,
		})
	}
	return tasks, resp.PageCount, nil
}

func (b p115OpenOfflineBackend) DeleteTask(ctx context.Context, id string) error {
	api := b.client()
	if api == nil {
		return drive.NewOfflineError(drive.OfflineErrProvider, "", "115_open: offline API is not initialized")
	}
	if err := b.driver.waitLimit(ctx); err != nil {
		return err
	}
	err := b.driver.recordSDK(ctx, "offline_delete", map[string]any{"id": id}, func() error {
		return api.DeleteOfflineTask(ctx, id, false)
	})
	if err != nil {
		if errors.Is(err, sdk.ErrObjectNotFound) {
			return p115offline.ErrTaskNotFound
		}
		return p115OpenOfflineError(err, false)
	}
	return nil
}

func (b p115OpenOfflineBackend) ListDir(ctx context.Context, dirID string) ([]drive.Entry, error) {
	if b.driver == nil {
		return nil, fmt.Errorf("115_open: offline driver is not initialized")
	}
	return b.driver.List(ctx, dirID)
}

func (b p115OpenOfflineBackend) DownloadTarget(ctx context.Context, fileID string, size int64) (drive.DownloadTarget, error) {
	api := b.client()
	if api == nil {
		return drive.DownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "115_open: offline API is not initialized")
	}
	if err := b.driver.waitLimit(ctx); err != nil {
		return drive.DownloadTarget{}, err
	}
	var info *sdk.GetFolderInfoResp
	err := b.driver.recordSDK(ctx, "offline_get_file", map[string]any{"id": fileID}, func() error {
		var err error
		info, err = api.GetFolderInfo(ctx, fileID)
		return err
	})
	if err != nil {
		return drive.DownloadTarget{}, p115OpenOfflineError(err, true)
	}
	if info == nil || info.PickCode == "" {
		return drive.DownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "115_open: staged file has no pick code")
	}
	var urls sdk.DownURLResp
	err = b.driver.recordSDK(ctx, "offline_download_url", map[string]any{"id": fileID}, func() error {
		var err error
		urls, err = api.DownURL(ctx, info.PickCode, downloadUserAgent)
		return err
	})
	if err != nil {
		return drive.DownloadTarget{}, p115OpenOfflineError(err, true)
	}
	if len(urls) == 0 {
		return drive.DownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProviderUnavailable, "", "115_open: download info has no URL")
	}
	if entry, ok := urls[fileID]; ok && entry.URL.URL != "" {
		if size == 0 {
			size = entry.FileSize
		}
		return drive.DownloadTarget{
			URL:     entry.URL.URL,
			Size:    size,
			Headers: map[string]string{"User-Agent": downloadUserAgent},
		}, nil
	}
	for _, entry := range urls {
		if entry.URL.URL == "" {
			continue
		}
		if size == 0 {
			size = entry.FileSize
		}
		return drive.DownloadTarget{
			URL:     entry.URL.URL,
			Size:    size,
			Headers: map[string]string{"User-Agent": downloadUserAgent},
		}, nil
	}
	return drive.DownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProviderUnavailable, "", "115_open: download info has no URL")
}

func (b p115OpenOfflineBackend) DefaultDirID() string {
	if b.driver == nil || b.driver.rootID == "" {
		return "0"
	}
	return b.driver.rootID
}

func p115OpenOfflineError(err error, observation bool) error {
	if err == nil {
		return nil
	}
	if offline, ok := drive.AsOfflineError(err); ok {
		return offline
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		return p115OpenOfflineCode(apiErr.Code, apiErr.Message)
	}
	if observation {
		return drive.NewOfflineError(drive.OfflineErrProviderUnavailable, "", "115_open: "+err.Error())
	}
	return drive.NewOfflineError(drive.OfflineErrProvider, "", "115_open: "+err.Error())
}

func p115OpenOfflineCode(code int64, message string) error {
	providerCode := fmt.Sprintf("%d", code)
	if message == "" {
		message = "115 open offline request rejected"
	}
	switch {
	case code == 10010:
		return drive.NewOfflineError(drive.OfflineErrQuotaExceeded, providerCode, message)
	case code == 10004:
		return drive.NewOfflineError(drive.OfflineErrSourceInvalid, providerCode, message)
	case code == 10008:
		return drive.NewOfflineError(drive.OfflineErrDuplicate, providerCode, message)
	case code == 99 || sdk.Is401Started(code):
		return drive.NewOfflineError(drive.OfflineErrAuthRequired, providerCode, message)
	default:
		return drive.NewOfflineError(drive.OfflineErrProvider, providerCode, message)
	}
}
