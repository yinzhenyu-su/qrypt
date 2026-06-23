package crypt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Driver wraps a raw backend with rclone-compatible name and content crypto.
type Driver struct {
	raw        drive.Driver
	cp         Cipher
	nonceCache sync.Map
}

func NewDriver(raw drive.Driver, cp Cipher) *Driver {
	return &Driver{raw: raw, cp: cp}
}

func (d *Driver) Init(ctx context.Context) error { return d.raw.Init(ctx) }

func (d *Driver) Drop(ctx context.Context) error { return d.raw.Drop(ctx) }

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	entries, err := d.raw.List(ctx, parentID)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		name, err := d.cp.DecryptSegment(entries[i].Name)
		if err == nil {
			entries[i].Name = strings.TrimSpace(name)
		}
		if !entries[i].IsDir {
			if size, err := d.cp.DecryptedSize(entries[i].Size); err == nil {
				entries[i].Size = size
			}
		}
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	rc, err := d.raw.Read(ctx, entry, 0, 0)
	if err != nil {
		return nil, err
	}
	header := make([]byte, FileHeaderSize)
	if _, err := io.ReadFull(rc, header); err != nil {
		rc.Close()
		return nil, fmt.Errorf("crypt: read header: %w", err)
	}
	if string(header[:FileMagicSize]) != FileMagic {
		rc.Close()
		return nil, errors.New("crypt: invalid rclone file header")
	}
	var nonce [FileNonceSize]byte
	copy(nonce[:], header[FileMagicSize:])
	reader := io.Reader(NewDecryptingReader(rc, d.cp, nonce))
	if offset > 0 {
		if _, err := io.CopyN(io.Discard, reader, offset); err != nil {
			rc.Close()
			return nil, err
		}
	}
	if size > 0 {
		reader = io.LimitReader(reader, size)
	}
	return struct {
		io.Reader
		io.Closer
	}{Reader: reader, Closer: rc}, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	writer, ok := d.raw.(drive.Writer)
	if !ok {
		return drive.Entry{}, errors.New("crypt: raw driver does not support mkdir")
	}
	entry, err := writer.Mkdir(ctx, parentID, d.cp.EncryptSegment(name))
	if err == nil {
		entry.Name = name
	}
	return entry, err
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	writer, ok := d.raw.(drive.Writer)
	if !ok {
		return errors.New("crypt: raw driver does not support move")
	}
	return writer.Move(ctx, entry, dstParentID)
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	writer, ok := d.raw.(drive.Writer)
	if !ok {
		return errors.New("crypt: raw driver does not support rename")
	}
	return writer.Rename(ctx, entry, d.cp.EncryptSegment(newName))
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	writer, ok := d.raw.(drive.Writer)
	if !ok {
		return errors.New("crypt: raw driver does not support remove")
	}
	return writer.Remove(ctx, entry)
}

func (d *Driver) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (drive.Entry, error) {
	uploader, ok := d.raw.(drive.Uploader)
	if !ok {
		return drive.Entry{}, errors.New("crypt: raw driver does not support upload")
	}
	nonce, err := d.cp.GenerateRandomNonce()
	if err != nil {
		return drive.Entry{}, fmt.Errorf("crypt: generate nonce: %w", err)
	}
	entry, err := uploader.Put(ctx, parentID, d.cp.EncryptSegment(name), d.cp.EncryptedSize(size), NewEncryptingReader(body, d.cp, nonce, size))
	if err == nil {
		d.nonceCache.Store(entry.ID, nonce)
		entry.Name = name
		entry.Size = size
	}
	return entry, err
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.Writer = (*Driver)(nil)
var _ drive.Uploader = (*Driver)(nil)
