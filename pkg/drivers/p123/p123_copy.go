package p123

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Copy implements drive.ServerSideCopier. 123pan exposes no copy endpoint
// (its /file/copy route is the share-link API and demands a ShareKey), but an
// upload request carrying the source's content MD5 resolves to 123pan's
// deduplicated blob and returns a new file id in the destination directory
// without transferring any bytes. The source file must live in the same
// account, so the blob is always already stored and the copy is instant.
func (d *Driver) Copy(ctx context.Context, src drive.Entry, dstParentID, dstName string) (drive.Entry, error) {
	if src.IsDir {
		return drive.Entry{}, drive.ErrUnsupported
	}
	// The copy caller (drivecopy) rebuilds the entry from stat info and drops
	// the provider metadata, so re-resolve it from a listing when absent.
	meta, _, err := d.refreshMeta(ctx, src)
	if err != nil {
		return drive.Entry{}, err
	}
	if meta.Etag == "" {
		return drive.Entry{}, drive.ErrUnsupported
	}
	dst := d.resolve(dstParentID)
	dstID, err := strconv.ParseInt(dst, 10, 64)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("123: copy: invalid destination id %q: %w", dst, drive.ErrInvalidInput)
	}

	up, err := d.cl.uploadRequest(ctx, dstID, dstName, meta.Size, strings.ToLower(meta.Etag))
	if err != nil {
		d.setLastError(err)
		return drive.Entry{}, fmt.Errorf("123: copy: %w", err)
	}
	if !up.Data.Reuse {
		// The source blob should already exist in this account. Without the
		// bytes there is no fallback, so report the operation unsupported
		// rather than uploading an empty or partial object.
		return drive.Entry{}, fmt.Errorf("123: copy: provider refused the instant copy: %w", drive.ErrUnsupported)
	}
	info, err := d.cl.uploadCompleteV2(ctx, up, meta.Size, false)
	if err != nil {
		d.setLastError(err)
		return drive.Entry{}, fmt.Errorf("123: copy: complete: %w", err)
	}
	entry, err := d.finalizeUpload(ctx, dst, dstName, meta.Size, meta.Etag, info)
	if err != nil {
		d.setLastError(err)
		return drive.Entry{}, fmt.Errorf("123: copy: %w", err)
	}
	return entry, nil
}
