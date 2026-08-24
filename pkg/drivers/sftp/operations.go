package sftp

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) List(ctx context.Context, parentID string) (entries []drive.Entry, err error) {
	started := time.Now()
	defer func() { d.recordOperation(ctx, "list", parentID, started, int64(len(entries)), err) }()
	client, err := d.getClient(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := d.resolveID(parentID)
	if err != nil {
		return nil, err
	}
	items, err := client.ReadDirContext(ctx, parent)
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
	if offset < 0 || size < 0 {
		err := fmt.Errorf("sftp: invalid read range offset=%d size=%d: %w", offset, size, drive.ErrInvalidInput)
		d.recordOperation(ctx, "read", entry.ID, started, 0, err)
		return nil, err
	}
	resolvedID, err := d.resolveID(entry.ID)
	if err != nil {
		d.recordOperation(ctx, "read", entry.ID, started, 0, err)
		return nil, err
	}
	if entry.Size > 0 && offset >= entry.Size {
		reader := io.NopCloser(strings.NewReader(""))
		return &metricReadCloser{ReadCloser: reader, d: d, ctx: ctx, operation: "read", path: entry.ID, offset: offset, requested: size, started: started}, nil
	}
	client, err := d.getClient(ctx)
	if err != nil {
		d.recordOperation(ctx, "read", entry.ID, started, 0, err)
		return nil, err
	}
	file, err := client.Open(resolvedID)
	if err != nil {
		err = fmt.Errorf("sftp: read %q: %w", resolvedID, classifyError(err))
		d.recordOperation(ctx, "read", entry.ID, started, 0, err)
		return nil, err
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		file.Close()
		err = fmt.Errorf("sftp: seek %q: %w", resolvedID, err)
		d.recordOperation(ctx, "read", entry.ID, started, 0, err)
		return nil, err
	}
	reader := contextReader{ctx: ctx, reader: file}
	var result io.ReadCloser
	if size > 0 {
		result = &limitedReadCloser{Reader: io.LimitReader(reader, size), closer: file}
	} else {
		result = &contextReadCloser{Reader: reader, closer: file}
	}
	result = d.limiter.LimitDownload(ctx, result)
	return &metricReadCloser{ReadCloser: result, d: d, ctx: ctx, operation: "read", path: entry.ID, offset: offset, requested: size, started: started}, nil
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
}

func (r *contextReadCloser) Close() error { return r.closer.Close() }

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type limitedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *limitedReadCloser) Close() error { return r.closer.Close() }
