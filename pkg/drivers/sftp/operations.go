package sftp

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const sftpOperationTimeout = 15 * time.Second

const sftpReadSize = 32 * 1024

const sftpUploadPartTimeout = 5 * time.Minute

func (d *Driver) List(ctx context.Context, parentID string) (entries []drive.Entry, err error) {
	started := time.Now()
	defer func() { d.recordOperation(ctx, "list", parentID, started, int64(len(entries)), err) }()
	opCtx, cancel := context.WithTimeout(ctx, sftpOperationTimeout)
	defer cancel()
	client, err := d.getClient(opCtx)
	if err != nil {
		return nil, err
	}
	parent, err := d.resolveID(parentID)
	if err != nil {
		return nil, err
	}
	items, err := d.readDir(opCtx, client, parent)
	if err != nil && isConnectionFailure(err) && opCtx.Err() == nil {
		client, reconnectErr := d.reconnect(opCtx, client)
		if reconnectErr == nil {
			items, err = d.readDir(opCtx, client, parent)
		} else {
			err = reconnectErr
		}
	}
	if err != nil {
		return nil, fmt.Errorf("sftp: list %q: %w", parent, classifyError(err))
	}
	entries = make([]drive.Entry, 0, len(items))
	for _, item := range items {
		itemPath := path.Join(parent, item.Name())
		entries = append(entries, drive.Entry{ID: itemPath, ParentID: parent, Name: item.Name(), IsDir: item.IsDir(), Size: item.Size(), ModTime: item.ModTime(), UpdatedAt: item.ModTime()})
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	started := time.Now()
	readCtx, cancelRead := context.WithTimeout(ctx, sftpOperationTimeout)
	if offset < 0 || size < 0 {
		cancelRead()
		err := fmt.Errorf("sftp: invalid read range offset=%d size=%d: %w", offset, size, drive.ErrInvalidInput)
		d.recordOperation(ctx, "read", entry.ID, started, 0, err)
		return nil, err
	}
	resolvedID, err := d.resolveID(entry.ID)
	if err != nil {
		cancelRead()
		d.recordOperation(ctx, "read", entry.ID, started, 0, err)
		return nil, err
	}
	if entry.Size > 0 && offset >= entry.Size {
		cancelRead()
		reader := io.NopCloser(strings.NewReader(""))
		return &metricReadCloser{ReadCloser: reader, d: d, ctx: ctx, operation: "read", path: entry.ID, offset: offset, requested: size, started: started}, nil
	}
	if entry.Size > 0 && (size <= 0 || offset+size > entry.Size) {
		size = entry.Size - offset
	}
	client, err := d.getClient(readCtx)
	if err != nil {
		cancelRead()
		d.recordOperation(ctx, "read", entry.ID, started, 0, err)
		return nil, err
	}
	file, err := d.openFile(readCtx, client, resolvedID)
	if err != nil {
		cancelRead()
		err = fmt.Errorf("sftp: read %q: %w", resolvedID, classifyError(err))
		d.recordOperation(ctx, "read", entry.ID, started, 0, err)
		return nil, err
	}
	if _, err := d.seekFile(readCtx, client, file, offset); err != nil {
		file.Close()
		cancelRead()
		err = fmt.Errorf("sftp: seek %q: %w", resolvedID, err)
		d.recordOperation(ctx, "read", entry.ID, started, 0, err)
		return nil, err
	}
	reader := newContextReadCloser(readCtx, contextReader{ctx: readCtx, reader: file}, file, func() {
		d.closeIfUnresponsive(client)
	})
	reader.cancel = cancelRead
	var result io.ReadCloser
	if size > 0 {
		result = &limitedReadCloser{Reader: io.LimitReader(reader, size), closer: reader}
	} else {
		result = reader
	}
	result = d.limiter.LimitDownload(ctx, result)
	return &metricReadCloser{ReadCloser: result, d: d, ctx: ctx, operation: "read", path: entry.ID, offset: offset, requested: size, started: started}, nil
}

func (d *Driver) openFile(ctx context.Context, client *sftp.Client, name string) (*sftp.File, error) {
	result := make(chan struct {
		file *sftp.File
		err  error
	}, 1)
	go func() {
		file, err := client.Open(name)
		result <- struct {
			file *sftp.File
			err  error
		}{file: file, err: err}
	}()
	select {
	case result := <-result:
		return result.file, result.err
	case <-ctx.Done():
		d.closeIfUnresponsive(client)
		return nil, ctx.Err()
	}
}

func (d *Driver) seekFile(ctx context.Context, client *sftp.Client, file *sftp.File, offset int64) (int64, error) {
	result := make(chan struct {
		offset int64
		err    error
	}, 1)
	go func() {
		position, err := file.Seek(offset, io.SeekStart)
		result <- struct {
			offset int64
			err    error
		}{offset: position, err: err}
	}()
	select {
	case result := <-result:
		return result.offset, result.err
	case <-ctx.Done():
		d.closeIfUnresponsive(client)
		return 0, ctx.Err()
	}
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (entry drive.Entry, err error) {
	started := time.Now()
	defer func() { d.recordOperation(ctx, "mkdir", path.Join(parentID, name), started, 0, err) }()
	if err := validateName(name); err != nil {
		return drive.Entry{}, err
	}
	client, err := d.getClient(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	parent, err := d.resolveID(parentID)
	if err != nil {
		return drive.Entry{}, err
	}
	child := path.Join(parent, name)
	if err := client.MkdirAll(child); err != nil {
		return drive.Entry{}, fmt.Errorf("sftp: mkdir %q: %w", child, classifyError(err))
	}
	return drive.Entry{ID: child, ParentID: parent, Name: path.Base(child), IsDir: true}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) (err error) {
	started := time.Now()
	defer func() { d.recordOperation(ctx, "move", entry.ID, started, 0, err) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := d.resolveID(entry.ID)
	if err != nil {
		return err
	}
	if err := validateName(entry.Name); err != nil {
		return err
	}
	client, err := d.getClient(ctx)
	if err != nil {
		return err
	}
	destinationParent, err := d.resolveID(dstParentID)
	if err != nil {
		return err
	}
	destination := path.Join(destinationParent, entry.Name)
	if err := client.Rename(source, destination); err != nil {
		return fmt.Errorf("sftp: move %q to %q: %w", source, destination, classifyError(err))
	}
	return nil
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) (err error) {
	started := time.Now()
	defer func() { d.recordOperation(ctx, "rename", entry.ID, started, 0, err) }()
	if err := validateName(newName); err != nil {
		return err
	}
	source, err := d.resolveID(entry.ID)
	if err != nil {
		return err
	}
	if source == d.rootPath {
		return fmt.Errorf("sftp: cannot rename root: %w", drive.ErrInvalidInput)
	}
	client, err := d.getClient(ctx)
	if err != nil {
		return err
	}
	destination := path.Join(path.Dir(source), newName)
	if err := client.Rename(source, destination); err != nil {
		return fmt.Errorf("sftp: rename %q to %q: %w", source, destination, classifyError(err))
	}
	return nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) (err error) {
	started := time.Now()
	defer func() { d.recordOperation(ctx, "remove", entry.ID, started, 0, err) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := d.resolveID(entry.ID)
	if err != nil {
		return err
	}
	if source == d.rootPath {
		return fmt.Errorf("sftp: cannot remove root: %w", drive.ErrInvalidInput)
	}
	client, err := d.getClient(ctx)
	if err != nil {
		return err
	}
	if err := client.RemoveAll(source); err != nil {
		return fmt.Errorf("sftp: remove %q: %w", source, classifyError(err))
	}
	return nil
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	if err := validateName(req.Name); err != nil {
		return drive.Entry{}, err
	}
	return d.putSourceResumable(ctx, req)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

type contextReadCloser struct {
	io.Reader
	closer io.Closer
	done   chan struct{}
	once   sync.Once
	err    error
	closed atomic.Bool
	cancel context.CancelFunc
}

func newContextReadCloser(ctx context.Context, reader io.Reader, closer io.Closer, onCancel func()) *contextReadCloser {
	result := &contextReadCloser{Reader: reader, closer: closer, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			if result.closed.Load() {
				return
			}
			if onCancel != nil {
				onCancel()
			}
			_ = result.Close()
		case <-result.done:
		}
	}()
	return result
}

func (r *contextReadCloser) Close() error {
	r.once.Do(func() {
		r.closed.Store(true)
		r.err = r.closer.Close()
		close(r.done)
		if r.cancel != nil {
			r.cancel()
		}
	})
	return r.err
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > sftpReadSize {
		p = p[:sftpReadSize]
	}
	return r.reader.Read(p)
}

type limitedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *limitedReadCloser) Close() error { return r.closer.Close() }
