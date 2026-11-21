package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// NewClientCore adapts a control-socket client to the Core surface the HTTP
// gateway serves. Production uses it in `qrypt http`; tests can build a
// gateway over a real Core without a second adapter.
func NewClientCore(client *control.Client) Core {
	return clientCore{client: client}
}

type clientCore struct {
	client *control.Client
}

func (c clientCore) RuntimeStatus(ctx context.Context) (control.RuntimeStatus, error) {
	return c.client.RuntimeStatus(ctx)
}

func (c clientCore) ListMounts(ctx context.Context) ([]control.MountStatus, error) {
	return c.client.ListMounts(ctx)
}

func (c clientCore) FSStat(ctx context.Context, requestPath string) (control.FSEntry, error) {
	return c.client.FSStat(ctx, requestPath)
}

func (c clientCore) FSList(ctx context.Context, requestPath string) ([]control.FSEntry, error) {
	return c.client.FSList(ctx, requestPath)
}

func (c clientCore) FSReadStream(ctx context.Context, requestPath string, offset, size int64, raw bool) (io.ReadCloser, error) {
	return c.client.FSReadStream(ctx, requestPath, offset, size, raw)
}

func (c clientCore) FSPendingUploads(ctx context.Context) ([]vfs.PendingUpload, error) {
	return c.client.FSPendingUploads(ctx)
}

func (c clientCore) FSCreate(ctx context.Context, requestPath string) error {
	return c.client.FSCreate(ctx, requestPath)
}

func (c clientCore) FSWriteAt(ctx context.Context, requestPath string, offset int64, data []byte) (int, error) {
	return c.client.FSWriteAt(ctx, requestPath, offset, data)
}

func (c clientCore) FSFlush(ctx context.Context, requestPath string) error {
	return c.client.FSFlush(ctx, requestPath)
}

func (c clientCore) FSMkdir(ctx context.Context, requestPath string) (control.FSEntry, error) {
	return c.client.FSMkdir(ctx, requestPath)
}

func (c clientCore) FSRemove(ctx context.Context, requestPath string, dir bool) error {
	return c.client.FSRemove(ctx, requestPath, dir)
}

func (c clientCore) FSRename(ctx context.Context, source, destination string) error {
	return c.client.FSRename(ctx, source, destination)
}

func (c clientCore) FSCopy(ctx context.Context, req control.FSCopyRequest) (json.RawMessage, error) {
	return c.client.FSCopy(ctx, req)
}

func (c clientCore) FSTruncate(ctx context.Context, requestPath string, size int64) error {
	return c.client.FSTruncate(ctx, requestPath, size)
}

func (c clientCore) FSWaitPath(ctx context.Context, requestPath string, timeout time.Duration) error {
	return c.client.FSWaitPath(ctx, requestPath, timeout)
}
