package vfs_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"io"
	"sync"
	"time"
)

type countingReadDriver struct {
	drive.UnsupportedOperations
	data    []byte
	id      string
	modTime time.Time
	mu      sync.Mutex
	read    map[int64]int
	sizes   map[int64]int64
	block   map[int64]chan struct{}
	entered map[int64]chan struct{}
}
type countingListDriver struct {
	drive.UnsupportedOperations
	mu    sync.Mutex
	lists map[string]int
}
type treeListDriver struct {
	drive.UnsupportedOperations
	mu      sync.Mutex
	lists   map[string]int
	entries map[string][]drive.Entry
	entered map[string]chan struct{}
	release map[string]chan struct{}
}
type metricHealthDriver struct {
	*countingReadDriver
	metrics []drive.MetricEvent
}

func testDriverSnapshot(name string) drive.DebugSnapshot {
	return drive.DebugSnapshot{Driver: name, Health: drive.HealthLevelOK, GeneratedAt: time.Now()}
}
func (d *countingReadDriver) Capabilities() []drive.Capability { return nil }
func (d *countingReadDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *countingReadDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("counting-read"), nil
}
func (d *countingReadDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func (d *countingListDriver) Capabilities() []drive.Capability { return nil }
func (d *countingListDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *countingListDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("counting-list"), nil
}
func (d *countingListDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func (d *treeListDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityWriter}
}
func (d *treeListDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *treeListDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("tree-list"), nil
}
func (d *treeListDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func (d *metricHealthDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return d.metrics, nil
}
func newCountingReadDriver(data []byte) *countingReadDriver {
	return &countingReadDriver{
		data:    data,
		modTime: time.Unix(123, 0).UTC(),
		read:    map[int64]int{},
		sizes:   map[int64]int64{},
		block:   map[int64]chan struct{}{},
		entered: map[int64]chan struct{}{},
	}
}
func (d *countingReadDriver) Init(context.Context) error { return nil }
func (d *countingReadDriver) Drop(context.Context) error { return nil }
func (d *countingReadDriver) List(context.Context, string) ([]drive.Entry, error) {
	id := d.id
	if id == "" {
		id = "file"
	}
	return []drive.Entry{{
		ID:       id,
		ParentID: "0",
		Name:     "data.bin",
		Size:     int64(len(d.data)),
		ModTime:  d.modTime,
	}}, nil
}
func (d *countingReadDriver) Read(ctx context.Context, _ drive.Entry, offset, size int64) (io.ReadCloser, error) {
	d.mu.Lock()
	d.read[offset]++
	d.sizes[offset] = size
	entered := d.entered[offset]
	block := d.block[offset]
	d.mu.Unlock()
	if entered != nil {
		select {
		case <-entered:
		default:
			close(entered)
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if offset >= int64(len(d.data)) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	end := offset + size
	if size <= 0 || end > int64(len(d.data)) {
		end = int64(len(d.data))
	}
	return io.NopCloser(bytes.NewReader(d.data[offset:end])), nil
}

type overReadDriver struct {
	*countingReadDriver
}

func (d *overReadDriver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	rc, err := d.countingReadDriver.Read(ctx, entry, offset, size)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	data = append(data, 'x')
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (d *countingReadDriver) readCount(offset int64) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.read[offset]
}
func (d *countingReadDriver) readSize(offset int64) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sizes[offset]
}
func (d *countingReadDriver) blockRead(offset int64) (entered chan struct{}, release func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	block := make(chan struct{})
	entered = make(chan struct{})
	d.block[offset] = block
	d.entered[offset] = entered
	return entered, func() { close(block) }
}
func (d *countingListDriver) Init(context.Context) error { return nil }
func (d *countingListDriver) Drop(context.Context) error { return nil }
func (d *countingListDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *countingListDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	d.mu.Lock()
	d.lists[parentID]++
	d.mu.Unlock()
	return []drive.Entry{{
		ID:       "child",
		ParentID: parentID,
		Name:     "child.txt",
		Size:     1,
	}}, nil
}
func (d *countingListDriver) listCount(parentID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lists[parentID]
}
func (d *treeListDriver) Init(context.Context) error { return nil }
func (d *treeListDriver) Drop(context.Context) error { return nil }
func (d *treeListDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *treeListDriver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	d.mu.Lock()
	d.lists[parentID]++
	entered := d.entered[parentID]
	release := d.release[parentID]
	entries := append([]drive.Entry(nil), d.entries[parentID]...)
	d.mu.Unlock()
	if entered != nil {
		closeOnce(entered)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return entries, nil
}
func (d *treeListDriver) listCount(parentID string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lists[parentID]
}
func (d *treeListDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	return drive.Entry{}, errors.New("mkdir should not be called")
}
func (d *treeListDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *treeListDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *treeListDriver) Remove(context.Context, drive.Entry) error {
	return nil
}
