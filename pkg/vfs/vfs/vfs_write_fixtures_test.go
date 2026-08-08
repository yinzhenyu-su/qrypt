package vfs_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"io"
	"strconv"
	"sync"
	"time"
)

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
type cancelAwareUploadDriver struct {
	drive.UnsupportedOperations
	mu       sync.Mutex
	entries  map[string]drive.Entry
	attempts int
	canceled bool
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
func (d *cancelAwareUploadDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySourceUploader}
}
func (d *cancelAwareUploadDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *cancelAwareUploadDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("cancel-aware-upload"), nil
}
func (d *cancelAwareUploadDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
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
func (d *cancelAwareUploadDriver) Init(context.Context) error { return nil }
func (d *cancelAwareUploadDriver) Drop(context.Context) error { return nil }
func (d *cancelAwareUploadDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
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
func (d *cancelAwareUploadDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *cancelAwareUploadDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	d.mu.Lock()
	d.attempts++
	d.mu.Unlock()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
	f, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	_, err = io.Copy(io.Discard, drive.NewUploadProgressReader(req.Progress, f))
	closeErr := f.Close()
	if err != nil {
		return drive.Entry{}, err
	}
	if closeErr != nil {
		return drive.Entry{}, closeErr
	}
	if err := ctx.Err(); err != nil {
		d.mu.Lock()
		d.canceled = true
		d.mu.Unlock()
		return drive.Entry{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.entries == nil {
		d.entries = map[string]drive.Entry{}
	}
	id := req.Name + "-" + strconv.Itoa(d.attempts)
	entry := drive.Entry{ID: id, ParentID: req.ParentID, Name: req.Name, Size: req.Source.Size()}
	d.entries[id] = entry
	return entry, nil
}
func (d *cancelAwareUploadDriver) state() (attempts int, canceled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.attempts, d.canceled
}
