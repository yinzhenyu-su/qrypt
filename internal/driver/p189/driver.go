package p189

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const timeFormat = "2006-01-02 15:04:05"
const sliceMD5Size = 1 << 20

type Driver struct {
	cl       *client
	rootID   int64
	rootPath string
	limiter  *drive.BandwidthLimiter
}

func init() {
	drive.Register("189", func(params drive.Params) (drive.Driver, error) {
		cookie := params["cookie"]
		username := params["username"]
		password := params["password"]
		if cookie == "" && (username == "" || password == "") {
			return nil, fmt.Errorf("189: missing cookie, or username and password")
		}
		d := &Driver{
			cl:       newClient(cookie, username, password),
			rootPath: params["root_path"],
		}
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
	if err := d.cl.loginInit(ctx); err != nil {
		return fmt.Errorf("189: login init: %w", err)
	}
	if d.cl.username != "" {
		if err := d.cl.getSessionKey(ctx); err != nil {
			return fmt.Errorf("189: get session key: %w", err)
		}
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

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	id, err := strconv.ParseInt(parentID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("189: invalid id: %w", err)
	}
	folders, files, err := d.cl.listFiles(ctx, id)
	if err != nil {
		return nil, err
	}
	entries := make([]drive.Entry, 0, len(folders)+len(files))
	for _, f := range folders {
		entries = append(entries, drive.Entry{
			ID:       strconv.FormatInt(f.ID, 10),
			ParentID: parentID,
			Name:     f.Name,
			IsDir:    true,
			ModTime:  parseTime(f.LastOpTime),
		})
	}
	for _, f := range files {
		entries = append(entries, drive.Entry{
			ID:       strconv.FormatInt(f.ID, 10),
			ParentID: parentID,
			Name:     f.Name,
			Size:     f.Size,
			ModTime:  parseTime(f.LastOpTime),
		})
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("189: invalid offset or size")
	}
	fileID, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("189: invalid file id: %w", err)
	}
	rawURL, err := d.cl.getDownloadURL(ctx, fileID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if size > 0 {
		end := offset + size - 1
		if entry.Size > 0 && end >= entry.Size {
			end = entry.Size - 1
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	} else if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("189: read: %w", err)
	}
	if resp.StatusCode == http.StatusPartialContent || resp.StatusCode == http.StatusOK {
		return d.limiter.LimitDownload(ctx, resp.Body), nil
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset >= entry.Size {
		resp.Body.Close()
		return io.NopCloser(strings.NewReader("")), nil
	}
	resp.Body.Close()
	return nil, fmt.Errorf("189: read: %s", resp.Status)
}

func (d *Driver) resolvePath(ctx context.Context, parentID int64, p string) (int64, error) {
	p = path.Clean(p)
	if p == "" || p == "." || p == "/" {
		return parentID, nil
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	currentID := parentID
	for _, part := range parts {
		folders, _, err := d.cl.listFiles(ctx, currentID)
		if err != nil {
			return 0, err
		}
		found := false
		for _, folder := range folders {
			if folder.Name == part {
				currentID = folder.ID
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("189: path %q not found", p)
		}
	}
	return currentID, nil
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	parent, err := strconv.ParseInt(parentID, 10, 64)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("189: invalid parent id: %w", err)
	}
	size := source.Size()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	fileMD5, err := sourceMD5Hex(ctx, source, size)
	if err != nil {
		return drive.Entry{}, err
	}
	sliceMD5, err := sourceSliceMD5Hex(ctx, source, size)
	if err != nil {
		return drive.Entry{}, err
	}
	uploadFileID, _, err := d.cl.initUpload(ctx, parent, name, size, fileMD5, sliceMD5)
	if err != nil {
		return drive.Entry{}, err
	}
	partCount := 1
	urls, err := d.cl.uploadData(ctx, uploadFileID, partCount)
	if err != nil {
		return drive.Entry{}, err
	}
	var partErr error
	for _, p := range urls {
		body, err := source.Open(ctx)
		if err != nil {
			partErr = fmt.Errorf("189: upload source open: %w", err)
			break
		}
		uploadBody := drive.NewUploadProgressReader(req.Progress, io.LimitReader(body, size))
		uploadBody = d.limiter.LimitUpload(ctx, uploadBody)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.RequestURL, uploadBody)
		if err != nil {
			body.Close()
			partErr = err
			break
		}
		req.ContentLength = size
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := d.cl.hc.Do(req)
		closeErr := body.Close()
		if err != nil {
			partErr = err
			break
		}
		resp.Body.Close()
		if closeErr != nil {
			partErr = closeErr
			break
		}
		if resp.StatusCode != http.StatusOK {
			partErr = fmt.Errorf("189: upload part: %s", resp.Status)
			break
		}
	}
	if partErr != nil {
		return drive.Entry{}, partErr
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	fileID, err := d.cl.commitUpload(ctx, uploadFileID)
	if err != nil {
		return drive.Entry{}, err
	}
	return drive.Entry{
		ID:       strconv.FormatInt(fileID, 10),
		ParentID: parentID,
		Name:     name,
		Size:     size,
	}, nil
}

func sourceMD5Hex(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (string, error) {
	if sum, ok := drive.SourceHash(source, drive.HashMD5); ok {
		if len(sum) != md5.Size {
			return "", fmt.Errorf("189: source MD5 metadata has %d bytes, want %d", len(sum), md5.Size)
		}
		return strings.ToUpper(hex.EncodeToString(sum)), nil
	}
	body, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("189: hash source open: %w", err)
	}
	defer body.Close()
	hash := md5.New()
	written, err := io.Copy(hash, body)
	if err != nil {
		return "", fmt.Errorf("189: hash source read: %w", err)
	}
	if written != size {
		return "", fmt.Errorf("189: hash source size mismatch: read %d, expected %d", written, size)
	}
	return strings.ToUpper(hex.EncodeToString(hash.Sum(nil))), nil
}

func sourceSliceMD5Hex(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (string, error) {
	if size <= sliceMD5Size {
		if sum, ok := drive.SourceHash(source, drive.HashMD5); ok {
			if len(sum) != md5.Size {
				return "", fmt.Errorf("189: source MD5 metadata has %d bytes, want %d", len(sum), md5.Size)
			}
			return strings.ToUpper(hex.EncodeToString(sum)), nil
		}
	}
	sliceLen := size
	if sliceLen > sliceMD5Size {
		sliceLen = sliceMD5Size
	}
	body, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("189: slice hash source open: %w", err)
	}
	defer body.Close()
	buf := make([]byte, sliceLen)
	n, err := body.ReadAt(buf, 0)
	if err != nil && !(err == io.EOF && int64(n) == sliceLen) {
		return "", fmt.Errorf("189: slice hash source read: %w", err)
	}
	if int64(n) != sliceLen {
		return "", fmt.Errorf("189: slice hash source size mismatch: read %d, expected %d", n, sliceLen)
	}
	sum := md5.Sum(buf)
	return fmt.Sprintf("%X", sum), nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID string, name string) (drive.Entry, error) {
	parent, err := strconv.ParseInt(parentID, 10, 64)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("189: invalid parent id: %w", err)
	}
	id, err := d.cl.createFolder(ctx, parent, name)
	if err != nil {
		return drive.Entry{}, err
	}
	return drive.Entry{
		ID:       strconv.FormatInt(id, 10),
		ParentID: parentID,
		Name:     name,
		IsDir:    true,
	}, nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	id, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("189: invalid id: %w", err)
	}
	isFolder := 0
	if entry.IsDir {
		isFolder = 1
	}
	taskInfos := fmt.Sprintf(`[{"fileId":%d,"isFolder":%d}]`, id, isFolder)
	return d.cl.batchTask(ctx, "DELETE", taskInfos, "")
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	id, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("189: invalid id: %w", err)
	}
	return d.cl.rename(ctx, id, newName, entry.IsDir)
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	id, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("189: invalid id: %w", err)
	}
	isFolder := 0
	if entry.IsDir {
		isFolder = 1
	}
	taskInfos := fmt.Sprintf(`[{"fileId":%d,"fileName":"","isFolder":%d}]`, id, isFolder)
	return d.cl.batchTask(ctx, "MOVE", taskInfos, dstParentID)
}

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	id, err := d.resolvePath(ctx, d.rootID, p)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	cap, err := d.cl.getCapacity(ctx)
	if err != nil {
		return drive.Space{}, err
	}
	return drive.Space{
		Total: cap.CloudCapacityInfo.TotalSize,
		Free:  cap.CloudCapacityInfo.FreeSize,
	}, nil
}

func parseTime(s string) time.Time {
	t, err := time.ParseInLocation(timeFormat, s, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}
