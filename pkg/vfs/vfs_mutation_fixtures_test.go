package vfs_test

import (
	"context"
	"errors"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"io"
	"sync"
	"time"
)

type countingRemoveDriver struct {
	drive.UnsupportedOperations
	mu          sync.Mutex
	entries     map[string]drive.Entry
	removed     []string
	mkdirs      []string
	failRemoves int
}
type staleMkdirListDriver struct {
	drive.UnsupportedOperations
	mu           sync.Mutex
	listCalls    map[string]int
	failFirstPut bool
	putAttempts  int
	lastParent   string
	lastName     string
	lastData     []byte
}
type staleMoveListDriver struct {
	drive.UnsupportedOperations
	renamed   []string
	moved     []string
	converged bool
}
type existingMkdirDriver struct {
	drive.UnsupportedOperations
	mkdirs int
	lists  int
}

func (d *countingRemoveDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityWriter}
}
func (d *countingRemoveDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *countingRemoveDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("counting-remove"), nil
}
func (d *countingRemoveDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func (d *staleMkdirListDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySourceUploader, drive.CapabilityWriter}
}
func (d *staleMkdirListDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *staleMkdirListDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("stale-mkdir-list"), nil
}
func (d *staleMkdirListDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func (d *staleMoveListDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityWriter}
}
func (d *staleMoveListDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *staleMoveListDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("stale-move-list"), nil
}
func (d *staleMoveListDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func (d *existingMkdirDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityWriter}
}
func (d *existingMkdirDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *existingMkdirDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("existing-mkdir"), nil
}
func (d *existingMkdirDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func newCountingRemoveDriver() *countingRemoveDriver {
	return &countingRemoveDriver{entries: map[string]drive.Entry{
		"dir": {ID: "dir", ParentID: "0", Name: "dir", IsDir: true},
		"a":   {ID: "a", ParentID: "dir", Name: "a.txt"},
		"sub": {ID: "sub", ParentID: "dir", Name: "sub", IsDir: true},
		"b":   {ID: "b", ParentID: "sub", Name: "b.txt"},
	}}
}
func (d *countingRemoveDriver) Init(context.Context) error { return nil }
func (d *countingRemoveDriver) Drop(context.Context) error { return nil }
func (d *countingRemoveDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *countingRemoveDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var entries []drive.Entry
	for _, entry := range d.entries {
		if entry.ParentID == parentID {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
func (d *countingRemoveDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mkdirs = append(d.mkdirs, "")
	return drive.Entry{}, errors.New("mkdir should not be called")
}
func (d *countingRemoveDriver) mkdirCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.mkdirs)
}
func (d *countingRemoveDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *countingRemoveDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *countingRemoveDriver) Remove(_ context.Context, entry drive.Entry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failRemoves > 0 {
		d.failRemoves--
		return errors.New("temporary remove failure")
	}
	d.removed = append(d.removed, entry.ID)
	for id, candidate := range d.entries {
		if id == entry.ID || isEntryUnder(candidate, entry.ID, d.entries) {
			delete(d.entries, id)
		}
	}
	return nil
}
func (d *countingRemoveDriver) removedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.removed...)
}
func isEntryUnder(entry drive.Entry, parentID string, entries map[string]drive.Entry) bool {
	for entry.ParentID != "" && entry.ParentID != "0" {
		if entry.ParentID == parentID {
			return true
		}
		parent, ok := entries[entry.ParentID]
		if !ok {
			return false
		}
		entry = parent
	}
	return false
}
func (d *staleMkdirListDriver) Init(context.Context) error { return nil }
func (d *staleMkdirListDriver) Drop(context.Context) error { return nil }
func (d *staleMkdirListDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *staleMkdirListDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	d.mu.Lock()
	if d.listCalls != nil {
		d.listCalls[parentID]++
	}
	d.mu.Unlock()
	return nil, nil
}
func (d *staleMkdirListDriver) Mkdir(_ context.Context, parentID, name string) (drive.Entry, error) {
	return drive.Entry{ID: "dir-id", ParentID: parentID, Name: name, IsDir: true}, nil
}
func (d *staleMkdirListDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *staleMkdirListDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *staleMkdirListDriver) Remove(context.Context, drive.Entry) error { return nil }
func (d *staleMkdirListDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	f, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return drive.Entry{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.putAttempts++
	if d.failFirstPut && d.putAttempts == 1 {
		return drive.Entry{}, errors.New("parent not ready")
	}
	d.lastParent = parentID
	d.lastName = name
	d.lastData = append(d.lastData[:0], data...)
	return drive.Entry{ID: name, ParentID: parentID, Name: name, Size: source.Size()}, nil
}
func (d *staleMkdirListDriver) lastPut() (attempts int, parent, name, data string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.putAttempts, d.lastParent, d.lastName, string(d.lastData)
}
func (d *staleMkdirListDriver) listCount(parentID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.listCalls[parentID]
}
func (d *staleMoveListDriver) Init(context.Context) error { return nil }
func (d *staleMoveListDriver) Drop(context.Context) error { return nil }
func (d *staleMoveListDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *staleMoveListDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	if d.converged {
		switch parentID {
		case "0":
			return []drive.Entry{{ID: "dir-id", ParentID: "0", Name: "video", IsDir: true}}, nil
		case "dir-id":
			return []drive.Entry{{ID: "movie-id", ParentID: "dir-id", Name: "movie.mp4", Size: 10}}, nil
		default:
			return nil, nil
		}
	}
	switch parentID {
	case "0":
		return []drive.Entry{
			{ID: "dir-id", ParentID: "0", Name: "新建文件夹", IsDir: true},
			{ID: "movie-id", ParentID: "0", Name: "movie.mp4", Size: 10},
		}, nil
	case "dir-id":
		return nil, nil
	default:
		return nil, nil
	}
}
func (d *staleMoveListDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	return drive.Entry{}, nil
}
func (d *staleMoveListDriver) Rename(_ context.Context, entry drive.Entry, newName string) error {
	d.renamed = append(d.renamed, entry.ID+":"+newName)
	return nil
}
func (d *staleMoveListDriver) Move(_ context.Context, entry drive.Entry, dstParentID string) error {
	d.moved = append(d.moved, entry.ID+":"+dstParentID)
	return nil
}
func (d *staleMoveListDriver) Remove(context.Context, drive.Entry) error { return nil }
func (d *existingMkdirDriver) Init(context.Context) error                { return nil }
func (d *existingMkdirDriver) Drop(context.Context) error                { return nil }
func (d *existingMkdirDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *existingMkdirDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	if parentID != "0" {
		return nil, nil
	}
	d.lists++
	if d.lists == 1 {
		return nil, nil
	}
	return []drive.Entry{{ID: "existing-dir", ParentID: "0", Name: "dir", IsDir: true}}, nil
}
func (d *existingMkdirDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	d.mkdirs++
	return drive.Entry{}, errors.New("already exists")
}
func (d *existingMkdirDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *existingMkdirDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *existingMkdirDriver) Remove(context.Context, drive.Entry) error { return nil }
