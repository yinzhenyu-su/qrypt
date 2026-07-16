package vfs_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/driver/localfs"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const testReadChunkSize = 512 * 1024
const testUploadDelay = 10 * time.Millisecond

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

type countingUploadDriver struct {
	drive.UnsupportedOperations
	mu          sync.Mutex
	uploads     int
	last        []byte
	entries     map[string]drive.Entry
	removed     []string
	renamed     []string
	failUploads int
	failRenames int
	blockReturn chan struct{}
	entered     chan struct{}
}

type blockingUploadDriver struct {
	drive.UnsupportedOperations
	mu      sync.Mutex
	uploads int
	entries map[string]drive.Entry
	entered chan struct{}
	release chan struct{}
}

type countingRemoveDriver struct {
	drive.UnsupportedOperations
	mu      sync.Mutex
	entries map[string]drive.Entry
	removed []string
	mkdirs  []string
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

type fileUploadDriver struct {
	drive.UnsupportedOperations
	mu             sync.Mutex
	entries        map[string]drive.Entry
	putCalls       int
	putSourceCalls int
	putSourceStart int
	sourceOpens    int
	lastData       []byte
	lastSHA256     []byte
	lastHasSHA256  bool
	allData        [][]byte
	blockFirst     chan struct{}
	firstEntered   chan struct{}
}

type sourceOnlyUploadDriver struct {
	drive.UnsupportedOperations
	mu       sync.Mutex
	entries  map[string]drive.Entry
	calls    int
	lastData []byte
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

func (d *countingUploadDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySourceUploader, drive.CapabilityWriter}
}
func (d *countingUploadDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *countingUploadDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("counting-upload"), nil
}
func (d *countingUploadDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

func (d *blockingUploadDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySourceUploader, drive.CapabilityWriter}
}
func (d *blockingUploadDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *blockingUploadDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("blocking-upload"), nil
}
func (d *blockingUploadDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
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

func (d *fileUploadDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySourceUploader}
}
func (d *fileUploadDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *fileUploadDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("file-upload"), nil
}
func (d *fileUploadDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

func (d *sourceOnlyUploadDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySourceUploader}
}
func (d *sourceOnlyUploadDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *sourceOnlyUploadDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("source-only-upload"), nil
}
func (d *sourceOnlyUploadDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
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

func newCountingRemoveDriver() *countingRemoveDriver {
	return &countingRemoveDriver{entries: map[string]drive.Entry{
		"dir": {ID: "dir", ParentID: "0", Name: "dir", IsDir: true},
		"a":   {ID: "a", ParentID: "dir", Name: "a.txt"},
		"sub": {ID: "sub", ParentID: "dir", Name: "sub", IsDir: true},
		"b":   {ID: "b", ParentID: "sub", Name: "b.txt"},
	}}
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

func (d *countingReadDriver) Read(_ context.Context, _ drive.Entry, offset, size int64) (io.ReadCloser, error) {
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
		<-block
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

func closeOnce(ch chan struct{}) {
	defer func() { _ = recover() }()
	close(ch)
}

func (d *countingUploadDriver) Init(context.Context) error { return nil }
func (d *countingUploadDriver) Drop(context.Context) error { return nil }
func (d *countingUploadDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
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
func (d *countingUploadDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *countingUploadDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
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
	if d.failUploads > 0 {
		d.failUploads--
		d.mu.Unlock()
		return drive.Entry{}, errors.New("temporary upload failure")
	}
	d.uploads++
	d.last = append(d.last[:0], data...)
	uploadID := name + "-" + strconv.Itoa(d.uploads)
	if d.entries == nil {
		d.entries = map[string]drive.Entry{}
	}
	size := source.Size()
	d.entries[uploadID] = drive.Entry{ID: uploadID, ParentID: parentID, Name: name, Size: size}
	block := d.blockReturn
	entered := d.entered
	d.blockReturn = nil
	d.entered = nil
	d.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if block != nil {
		<-block
	}
	return drive.Entry{ID: uploadID, ParentID: parentID, Name: name, Size: size}, nil
}
func (d *countingUploadDriver) uploadCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.uploads
}
func (d *countingUploadDriver) lastUpload() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.last)
}
func (d *countingUploadDriver) removedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.removed...)
}
func (d *countingUploadDriver) renamedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.renamed...)
}
func (d *countingUploadDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	return drive.Entry{}, errors.New("mkdir should not be called")
}
func (d *countingUploadDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *countingUploadDriver) Rename(_ context.Context, entry drive.Entry, newName string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failRenames > 0 {
		d.failRenames--
		return errors.New("temporary rename failure")
	}
	existing := d.entries[entry.ID]
	existing.Name = newName
	d.entries[entry.ID] = existing
	d.renamed = append(d.renamed, entry.ID+":"+newName)
	return nil
}
func (d *countingUploadDriver) Remove(_ context.Context, entry drive.Entry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.entries, entry.ID)
	d.removed = append(d.removed, entry.ID)
	return nil
}

func newBlockingUploadDriver() *blockingUploadDriver {
	return &blockingUploadDriver{
		entries: map[string]drive.Entry{},
		entered: make(chan struct{}, 16),
		release: make(chan struct{}),
	}
}

func (d *blockingUploadDriver) Init(context.Context) error { return nil }
func (d *blockingUploadDriver) Drop(context.Context) error { return nil }
func (d *blockingUploadDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
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
func (d *blockingUploadDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *blockingUploadDriver) Mkdir(context.Context, string, string) (drive.Entry, error) {
	return drive.Entry{}, errors.New("mkdir should not be called")
}
func (d *blockingUploadDriver) Move(context.Context, drive.Entry, string) error { return nil }
func (d *blockingUploadDriver) Rename(context.Context, drive.Entry, string) error {
	return nil
}
func (d *blockingUploadDriver) Remove(_ context.Context, entry drive.Entry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.entries, entry.ID)
	return nil
}
func (d *blockingUploadDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
	f, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	defer f.Close()
	if _, err := io.Copy(io.Discard, drive.NewUploadProgressReader(req.Progress, f)); err != nil {
		return drive.Entry{}, err
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	d.mu.Lock()
	d.uploads++
	id := name + "-" + strconv.Itoa(d.uploads)
	entry := drive.Entry{ID: id, ParentID: parentID, Name: name, Size: source.Size()}
	d.entries[id] = entry
	d.mu.Unlock()
	d.entered <- struct{}{}
	<-d.release
	return entry, nil
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

func (d *existingMkdirDriver) Init(context.Context) error { return nil }
func (d *existingMkdirDriver) Drop(context.Context) error { return nil }
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

func (d *fileUploadDriver) Init(context.Context) error { return nil }
func (d *fileUploadDriver) Drop(context.Context) error { return nil }
func (d *fileUploadDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
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
func (d *fileUploadDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *fileUploadDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	d.mu.Lock()
	d.putSourceStart++
	call := d.putSourceStart
	entered := d.firstEntered
	block := d.blockFirst
	d.mu.Unlock()
	if call == 1 && entered != nil {
		close(entered)
	}
	if call == 1 && block != nil {
		<-block
	}
	sourceSize := source.Size()
	sourceSHA256, hasSHA256 := drive.SourceHash(source, drive.HashSHA256)
	f, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	data, err := io.ReadAll(f)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err == nil && len(data) > 0 {
		buf := make([]byte, 1)
		_, err = f.ReadAt(buf, int64(len(data)-1))
		if err == nil && buf[0] != data[len(data)-1] {
			err = fmt.Errorf("ReadAt last byte=%q, want %q", buf[0], data[len(data)-1])
		}
	}
	closeErr := f.Close()
	if err != nil {
		return drive.Entry{}, err
	}
	if sourceSize != int64(len(data)) {
		return drive.Entry{}, fmt.Errorf("source size=%d, read %d", sourceSize, len(data))
	}
	if closeErr != nil {
		return drive.Entry{}, closeErr
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.putSourceCalls++
	d.sourceOpens++
	d.lastData = append(d.lastData[:0], data...)
	d.lastSHA256 = append(d.lastSHA256[:0], sourceSHA256...)
	d.lastHasSHA256 = hasSHA256
	d.allData = append(d.allData, append([]byte(nil), data...))
	return d.saveEntryLocked(parentID, name, source.Size()), nil
}
func (d *fileUploadDriver) saveEntryLocked(parentID, name string, size int64) drive.Entry {
	if d.entries == nil {
		d.entries = map[string]drive.Entry{}
	}
	id := name + "-" + strconv.Itoa(d.putCalls+d.putSourceCalls)
	entry := drive.Entry{ID: id, ParentID: parentID, Name: name, Size: size}
	d.entries[id] = entry
	return entry
}

func (d *sourceOnlyUploadDriver) Init(context.Context) error { return nil }
func (d *sourceOnlyUploadDriver) Drop(context.Context) error { return nil }
func (d *sourceOnlyUploadDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
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
func (d *sourceOnlyUploadDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *sourceOnlyUploadDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	f, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	data, err := io.ReadAll(f)
	closeErr := f.Close()
	if err != nil {
		return drive.Entry{}, err
	}
	if closeErr != nil {
		return drive.Entry{}, closeErr
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.lastData = append(d.lastData[:0], data...)
	if d.entries == nil {
		d.entries = map[string]drive.Entry{}
	}
	id := name + "-" + strconv.Itoa(d.calls)
	entry := drive.Entry{ID: id, ParentID: parentID, Name: name, Size: source.Size()}
	d.entries[id] = entry
	return entry, nil
}

func waitNoPending(t *testing.T, fs vfs.FileSystem) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(fs.Pending()) == 0 && activeUploadCount(fs) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending uploads did not drain: %+v", fs.Pending())
}

func activeUploadCount(fs vfs.FileSystem) int {
	snapshotter, ok := fs.(interface {
		DebugSnapshot() vfs.DebugSnapshot
	})
	if !ok {
		return 0
	}
	count := 0
	for _, mount := range snapshotter.DebugSnapshot().Mounts {
		count += len(mount.ActiveUploads())
	}
	return count
}

func waitForCondition(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition was not met before deadline")
}

func singleMountHealth(t *testing.T, fs *vfs.VFS) vfs.MountHealth {
	t.Helper()
	health, err := fs.MountHealth(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(health) != 1 {
		t.Fatalf("mount health count = %d, want 1: %+v", len(health), health)
	}
	return health[0]
}

func assertHealthOp(t *testing.T, health vfs.MountHealth, op string, success, failures int) {
	t.Helper()
	got, ok := health.Ops[op]
	if !ok {
		t.Fatalf("missing health op %q in %+v", op, health.Ops)
	}
	if got.Success != success || got.Errors != failures {
		t.Fatalf("%s health = %+v, want success=%d errors=%d", op, got, success, failures)
	}
}

func namesOf(entries []drive.Entry) string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return strings.Join(names, ",")
}

var _ drive.Driver = (*localfs.Driver)(nil)
