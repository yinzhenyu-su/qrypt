package p115

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/p115offline"
)

// p115OfflineAPI is the SDK surface the offline backend uses. It keeps the
// adapter testable without the 115 request encryption used by add-task.
type p115OfflineAPI interface {
	AddOfflineTaskURIs(uris []string, saveDirID string, opts ...driver115.OfflineOption) ([]string, error)
	ListOfflineTask(page int64) (driver115.OfflineTaskResp, error)
	DeleteOfflineTasks(hashes []string, deleteFiles bool) error
	GetFile(id string) (*driver115.File, error)
	DownloadWithUA(pickCode, ua string) (*driver115.DownloadInfo, error)
}

type p115OfflineBackend struct {
	driver *Driver
	api    p115OfflineAPI
}

func (d *Driver) offline() *p115offline.Adapter {
	d.offlineOnce.Do(func() {
		d.offlineAdapter = p115offline.New(p115OfflineBackend{driver: d})
	})
	return d.offlineAdapter
}

func (b p115OfflineBackend) client() p115OfflineAPI {
	if b.api != nil {
		return b.api
	}
	if b.driver != nil {
		return b.driver.cl
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

func (b p115OfflineBackend) AddTask(ctx context.Context, source, dirID string) (string, error) {
	api := b.client()
	if api == nil {
		return "", drive.NewOfflineError(drive.OfflineErrProvider, "", "115: offline API is not initialized")
	}
	if err := b.driver.waitLimit(ctx); err != nil {
		return "", err
	}
	var hashes []string
	err := b.driver.recordSDK(ctx, "offline_add", map[string]any{"dir_id": dirID}, func() error {
		var err error
		hashes, err = api.AddOfflineTaskURIs([]string{source}, dirID, driver115.WithAppVer(appVer))
		return err
	})
	if err != nil {
		return "", p115OfflineError(err, false)
	}
	if len(hashes) == 0 || strings.TrimSpace(hashes[0]) == "" {
		return "", drive.NewOfflineError(drive.OfflineErrProvider, "", "115: add task was rejected")
	}
	return strings.TrimSpace(hashes[0]), nil
}

func (b p115OfflineBackend) ListTasks(ctx context.Context, page int) ([]p115offline.Task, int, error) {
	api := b.client()
	if api == nil {
		return nil, 0, drive.NewOfflineError(drive.OfflineErrProvider, "", "115: offline API is not initialized")
	}
	if err := b.driver.waitLimit(ctx); err != nil {
		return nil, 0, err
	}
	var resp driver115.OfflineTaskResp
	err := b.driver.recordSDK(ctx, "offline_list", map[string]any{"page": page}, func() error {
		var err error
		// The cookie task-list endpoint is zero-based; the shared adapter uses
		// one-based pages.
		resp, err = api.ListOfflineTask(int64(page - 1))
		return err
	})
	if err != nil {
		return nil, 0, p115OfflineError(err, true)
	}
	tasks := make([]p115offline.Task, 0, len(resp.Tasks))
	for _, task := range resp.Tasks {
		if task == nil {
			continue
		}
		tasks = append(tasks, p115offline.Task{
			ID:       task.InfoHash,
			Name:     task.Name,
			Size:     task.Size,
			Status:   task.Status,
			Percent:  task.Percent,
			FileID:   task.FileId,
			ParentID: task.DirId,
			URL:      task.Url,
		})
	}
	return tasks, int(resp.PageCount), nil
}

func (b p115OfflineBackend) DeleteTask(ctx context.Context, id string) error {
	api := b.client()
	if api == nil {
		return drive.NewOfflineError(drive.OfflineErrProvider, "", "115: offline API is not initialized")
	}
	if err := b.driver.waitLimit(ctx); err != nil {
		return err
	}
	err := b.driver.recordSDK(ctx, "offline_delete", map[string]any{"id": id}, func() error {
		return api.DeleteOfflineTasks([]string{id}, false)
	})
	if err != nil {
		if errors.Is(err, driver115.ErrNotExist) {
			return p115offline.ErrTaskNotFound
		}
		return p115OfflineError(err, false)
	}
	return nil
}

func (b p115OfflineBackend) ListDir(ctx context.Context, dirID string) ([]drive.Entry, error) {
	if b.driver == nil {
		return nil, fmt.Errorf("115: offline driver is not initialized")
	}
	return b.driver.List(ctx, dirID)
}

func (b p115OfflineBackend) DownloadTarget(ctx context.Context, fileID string, size int64) (drive.DownloadTarget, error) {
	api := b.client()
	if api == nil {
		return drive.DownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "115: offline API is not initialized")
	}
	if err := b.driver.waitLimit(ctx); err != nil {
		return drive.DownloadTarget{}, err
	}
	var file *driver115.File
	err := b.driver.recordSDK(ctx, "offline_get_file", map[string]any{"id": fileID}, func() error {
		var err error
		file, err = api.GetFile(fileID)
		return err
	})
	if err != nil {
		if errors.Is(err, driver115.ErrDownloadFileNotExistOrHasDeleted) || strings.Contains(err.Error(), "文件不存在") {
			return drive.DownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "115: staged file is no longer available")
		}
		return drive.DownloadTarget{}, p115OfflineError(err, true)
	}
	if file == nil || file.PickCode == "" {
		return drive.DownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProvider, "", "115: staged file has no pick code")
	}
	var info *driver115.DownloadInfo
	err = b.driver.recordSDK(ctx, "offline_download_url", map[string]any{"id": fileID}, func() error {
		var err error
		info, err = api.DownloadWithUA(file.PickCode, b.driver.userAgent())
		return err
	})
	if err != nil {
		return drive.DownloadTarget{}, p115OfflineError(err, true)
	}
	if info == nil || info.Url.Url == "" {
		return drive.DownloadTarget{}, drive.NewOfflineError(drive.OfflineErrProviderUnavailable, "", "115: download info has no URL")
	}
	if size == 0 && info.FileSize > 0 {
		size = int64(info.FileSize)
	}
	headers := make(map[string]string, len(info.Header))
	for key, values := range info.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	return drive.DownloadTarget{URL: info.Url.Url, Size: size, Headers: headers}, nil
}

func (b p115OfflineBackend) DefaultDirID() string {
	if b.driver == nil || b.driver.rootID == "" {
		return "0"
	}
	return b.driver.rootID
}

// p115OfflineError maps the cookie SDK's offline sentinels onto the stable
// provider-neutral codes. observation errors use the retryable unavailable
// code because they are polled; create/delete errors stay non-retryable unless
// they are explicitly transient.
func p115OfflineError(err error, observation bool) error {
	if err == nil {
		return nil
	}
	if offline, ok := drive.AsOfflineError(err); ok {
		return offline
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, driver115.ErrOfflineNoTimes):
		return drive.NewOfflineError(drive.OfflineErrQuotaExceeded, "", "115: offline quota is exhausted")
	case errors.Is(err, driver115.ErrOfflineInvalidLink):
		return drive.NewOfflineError(drive.OfflineErrSourceInvalid, "", "115: invalid offline source")
	case errors.Is(err, driver115.ErrOfflineTaskExisted):
		return drive.NewOfflineError(drive.OfflineErrDuplicate, "", "115: offline task already exists")
	case errors.Is(err, driver115.ErrNotLogin),
		errors.Is(err, driver115.ErrCredentialInvalid),
		errors.Is(err, driver115.ErrSessionExited),
		errors.Is(err, driver115.ErrDoesLoggedOut),
		errors.Is(err, driver115.ErrBadCookie):
		return drive.NewOfflineError(drive.OfflineErrAuthRequired, "", "115: authentication is required")
	}
	if code := p115ProviderCode(err.Error()); code != 0 {
		return p115OfflineCode(code, err.Error())
	}
	if observation {
		return drive.NewOfflineError(drive.OfflineErrProviderUnavailable, "", "115: "+p115ErrorDetail(err.Error()))
	}
	return drive.NewOfflineError(drive.OfflineErrProvider, "", "115: "+p115ErrorDetail(err.Error()))
}

// p115ProviderCode extracts the provider code from the SDK's wrapped error
// body. The SDK normalizes only errno/errNo, while the current offline add
// endpoint reports errcode, so the raw body is the only place the code is
// available.
func p115ProviderCode(message string) int {
	for _, key := range []string{`"errcode":`, `"errno":`, `"errNo":`} {
		index := strings.Index(message, key)
		if index < 0 {
			continue
		}
		rest := strings.TrimSpace(message[index+len(key):])
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end > 0 {
			code, err := strconv.Atoi(rest[:end])
			if err == nil {
				return code
			}
		}
	}
	return 0
}

func p115OfflineCode(code int, message string) error {
	providerCode := strconv.Itoa(code)
	switch code {
	case 10010:
		return drive.NewOfflineError(drive.OfflineErrQuotaExceeded, providerCode, "115: offline quota is exhausted")
	case 10004:
		return drive.NewOfflineError(drive.OfflineErrSourceInvalid, providerCode, "115: invalid offline source")
	case 10008:
		return drive.NewOfflineError(drive.OfflineErrDuplicate, providerCode, "115: offline task already exists")
	case 99, 990001:
		return drive.NewOfflineError(drive.OfflineErrAuthRequired, providerCode, "115: authentication is required")
	default:
		return drive.NewOfflineError(drive.OfflineErrProvider, providerCode, "115: "+p115ErrorDetail(message))
	}
}

// p115ErrorDetail keeps the provider's readable message and avoids putting an
// entire encrypted response body into the task detail.
func p115ErrorDetail(message string) string {
	const key = `"error_msg":"`
	if index := strings.Index(message, key); index >= 0 {
		rest := message[index+len(key):]
		if end := strings.IndexByte(rest, '"'); end >= 0 {
			return rest[:end]
		}
	}
	if len(message) > 300 {
		return message[:300] + "..."
	}
	return message
}
