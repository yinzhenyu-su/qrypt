package p189

import (
	"bytes"
	"context"
	"crypto/md5"
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

type Driver struct {
	cl       *client
	rootID   int64
	rootPath string
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
		return resp.Body, nil
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

func (d *Driver) Put(ctx context.Context, parentID string, name string, size int64, body io.Reader) (drive.Entry, error) {
	parent, err := strconv.ParseInt(parentID, 10, 64)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("189: invalid parent id: %w", err)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("189: read body: %w", err)
	}
	actualSize := int64(len(data))
	fileMd5 := md5.Sum(data)
	sliceLen := actualSize
	if sliceLen > 1048576 {
		sliceLen = 1048576
	}
	sliceMd5 := md5.Sum(data[:sliceLen])
	uploadFileID, _, err := d.cl.initUpload(ctx, parent, name, actualSize,
		fmt.Sprintf("%X", fileMd5), fmt.Sprintf("%X", sliceMd5))
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
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.RequestURL, bytes.NewReader(data))
		if err != nil {
			partErr = err
			break
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			partErr = err
			break
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			partErr = fmt.Errorf("189: upload part: %s", resp.Status)
			break
		}
	}
	if partErr != nil {
		return drive.Entry{}, partErr
	}
	fileID, err := d.cl.commitUpload(ctx, uploadFileID)
	if err != nil {
		return drive.Entry{}, err
	}
	return drive.Entry{
		ID:       strconv.FormatInt(fileID, 10),
		ParentID: parentID,
		Name:     name,
		Size:     actualSize,
	}, nil
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
