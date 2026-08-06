package drive

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"
)

// FakeDriver is an in-memory Driver for tests and tools. It implements the
// full Driver contract against a memory file tree so business-logic tests
// (VFS state machines, core tasks, CLI sync planning) do not need a real
// backend, while error/delay injection lets tests exercise failure paths
// that localfs cannot produce.
//
// It deliberately keeps real filesystem semantics where they matter:
// List returns children sorted by name, Read honors offsets/EOF and context
// cancellation, PutSource replaces the target object, Move/Rename validate
// existence, and Remove classifies missing objects as ErrNotFound.
type FakeDriver struct {
	mu sync.Mutex

	nodes    map[string]*fakeNode // key: object id (path-based)
	rootID   string
	nextID   int64
	space    Space
	hasSpace bool

	caps []Capability
	// Err* inject a failure for one method call (cleared after use).
	// The injected error is returned as-is; returning a sentinel like
	// ErrNotFound flows through ErrorCategory exactly like a real driver.
	ErrList      error
	ErrRead      error
	ErrMkdir     error
	ErrMove      error
	ErrRename    error
	ErrRemove    error
	ErrPutSource error
	ErrResolve   error
	ErrSpace     error
	// Delay pauses every method (including ctx checks) so timeout and
	// cancellation paths are testable. ReadDelay applies to Read only.
	Delay     time.Duration
	ReadDelay time.Duration
	// PutDelay applies to PutSource only.
	PutDelay time.Duration

	// Calls records every method invocation as "Method:arg" so tests can
	// assert call counts and sequences.
	Calls []string

	// FailAfter schedules the Nth invocation of a method (1-based) to fail
	// with err. Earlier invocations succeed; the fault fires exactly once
	// and then clears. Unlike Err*, which fails the next call, FailAfter
	// lets a test land a fault exactly when a retry arrives. The counter
	// lives on the driver, so it survives across VFS instances that share
	// this driver (upload-recovery-after-failure scenarios).
	failAfter map[string]failAt

	// ListStaleness > 0 makes List return the tree as it was N mutations
	// ago, emulating eventually-consistent backends (quark, p115). Zero is
	// immediate consistency. Every mutation captures a fresh snapshot, so
	// N must stay small (test trees).
	ListStaleness int

	// Gate, when non-nil, makes every method call block until it receives
	// one token or ctx is done. Tests send a value to release one in-flight
	// call, or cancel ctx to simulate a hung provider mid-operation.
	Gate chan struct{}

	// snapshots is the mutation history backing ListStaleness;
	// snapshots[len-1] is the current tree.
	snapshots []map[string][]Entry
}

type failAt struct {
	remaining int
	err       error
}

type fakeNode struct {
	name     string
	isDir    bool
	data     []byte
	modTime  time.Time
	parentID string
}

// FakeCallOption configures a FakeDriver. The zero value is usable: a
// read-write driver over an empty tree rooted at "root".
type FakeCallOption func(*FakeDriver)

// FakeWithCapabilities sets the driver's declared optional capabilities.
func FakeWithCapabilities(caps ...Capability) FakeCallOption {
	return func(d *FakeDriver) { d.caps = caps }
}

// FakeWithRootID overrides the root object id (default "0", matching the
// VFS default RootID).
func FakeWithRootID(id string) FakeCallOption {
	return func(d *FakeDriver) { d.rootID = id }
}

// FakeWithSpace makes Space report the given bytes instead of ErrSpaceUnsupported.
func FakeWithSpace(total, free int64) FakeCallOption {
	return func(d *FakeDriver) {
		d.space = Space{Total: total, Free: free}
		d.hasSpace = true
	}
}

// NewFakeDriver builds an in-memory driver. The tree is empty; use
// PutSource/Mkdir through the Driver interface or Seed to pre-populate it.
func NewFakeDriver(opts ...FakeCallOption) *FakeDriver {
	d := &FakeDriver{
		nodes:  map[string]*fakeNode{},
		rootID: "0", // matches vfs.Options.RootID's default
		caps:   []Capability{CapabilityWriter, CapabilitySourceUploader, CapabilityPathResolver, CapabilityRemoteNameResolver},
	}
	d.nodes[d.rootID] = &fakeNode{name: "root", isDir: true, parentID: ""}
	for _, opt := range opts {
		opt(d)
	}
	d.captureLocked()
	return d
}

// Seed inserts a plain file tree into the driver (used by callers that want
// a pre-populated backend without going through the Driver interface).
// Keys are virtual paths like "dir/file.txt"; values are file contents.
// Directories are created implicitly.
func (d *FakeDriver) Seed(files map[string]string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for virtualPath, content := range files {
		isDir := strings.HasSuffix(virtualPath, "/")
		parts := strings.Split(strings.Trim(virtualPath, "/"), "/")
		if isDir {
			parts = append(parts, "") // placeholder so the dir itself is created
		}
		parent := d.rootID
		for _, part := range parts[:len(parts)-1] {
			dirID := parent + "/" + part
			if d.nodes[dirID] == nil {
				d.nodes[dirID] = &fakeNode{name: part, isDir: true, parentID: parent}
			} else if !d.nodes[dirID].isDir {
				return fmt.Errorf("fake: %q is not a directory", part)
			}
			parent = dirID
		}
		name := parts[len(parts)-1]
		if isDir {
			// "dir/" entries create the directory and nothing else.
			continue
		}
		id := parent + "/" + name
		if d.nodes[id] == nil {
			d.nodes[id] = &fakeNode{name: name, parentID: parent}
		}
		d.nodes[id].data = []byte(content)
		d.nodes[id].modTime = time.Now()
	}
	d.captureLocked()
	return nil
}

// FakeCalls returns a copy of the call log.
func (d *FakeDriver) FakeCalls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.Calls...)
}

func (d *FakeDriver) record(method, arg string) {
	d.Calls = append(d.Calls, method+":"+arg)
}

// FailAfter schedules the Nth invocation (1-based) of method to fail with
// err. Earlier invocations succeed. The fault fires exactly once and then
// clears.
func (d *FakeDriver) FailAfter(method string, n int, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failAfter == nil {
		d.failAfter = make(map[string]failAt)
	}
	d.failAfter[method] = failAt{remaining: n, err: err}
}

func (d *FakeDriver) failOn(method string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.failAfter[method]
	if !ok {
		return nil
	}
	f.remaining--
	if f.remaining > 0 {
		d.failAfter[method] = f
		return nil
	}
	delete(d.failAfter, method)
	return f.err
}

func (d *FakeDriver) gate() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Gate
}

// captureLocked snapshots the whole tree. Caller must hold d.mu.
func (d *FakeDriver) captureLocked() {
	entries := make(map[string][]Entry)
	for id, n := range d.nodes {
		entries[n.parentID] = append(entries[n.parentID], Entry{
			ID:       id,
			ParentID: n.parentID,
			Name:     n.name,
			IsDir:    n.isDir,
			Size:     int64(len(n.data)),
			ModTime:  n.modTime,
		})
	}
	for pid := range entries {
		sort.Slice(entries[pid], func(i, j int) bool { return entries[pid][i].Name < entries[pid][j].Name })
	}
	d.snapshots = append(d.snapshots, entries)
}

func (d *FakeDriver) wait(ctx context.Context, delay time.Duration) error {
	if g := d.gate(); g != nil {
		select {
		case <-g:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if delay > 0 {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (d *FakeDriver) consume(hook *error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	err := *hook
	*hook = nil
	return err
}

// Init implements Driver.
func (d *FakeDriver) Init(ctx context.Context) error {
	d.mu.Lock()
	d.record("Init", "")
	d.mu.Unlock()
	if err := d.failOn("Init"); err != nil {
		return err
	}
	return d.wait(ctx, d.Delay)
}

// Drop implements Driver.
func (d *FakeDriver) Drop(ctx context.Context) error {
	d.mu.Lock()
	d.record("Drop", "")
	d.mu.Unlock()
	if err := d.failOn("Drop"); err != nil {
		return err
	}
	return d.wait(ctx, d.Delay)
}

// List implements Driver: children of parentID, sorted by name.
func (d *FakeDriver) List(ctx context.Context, parentID string) ([]Entry, error) {
	d.mu.Lock()
	d.record("List", parentID)
	d.mu.Unlock()
	if err := d.failOn("List"); err != nil {
		return nil, err
	}
	if err := d.wait(ctx, d.Delay); err != nil {
		return nil, err
	}
	if err := d.consume(&d.ErrList); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// Serve a stale snapshot when ListStaleness is configured; the current
	// tree is always the last snapshot.
	idx := len(d.snapshots) - 1 - d.ListStaleness
	if idx < 0 {
		idx = 0
	}
	if entries, ok := d.snapshots[idx][parentID]; ok {
		return append([]Entry(nil), entries...), nil
	}
	if _, exists := d.nodes[parentID]; !exists {
		return nil, fmt.Errorf("%w: fake list parent %q", ErrNotFound, parentID)
	}
	return nil, nil
}

// Read implements Driver with offset/size semantics and EOF.
func (d *FakeDriver) Read(ctx context.Context, entry Entry, offset, size int64) (io.ReadCloser, error) {
	d.mu.Lock()
	d.record("Read", entry.ID)
	d.mu.Unlock()
	if err := d.failOn("Read"); err != nil {
		return nil, err
	}
	if err := d.wait(ctx, d.Delay+d.ReadDelay); err != nil {
		return nil, err
	}
	if err := d.consume(&d.ErrRead); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	n, ok := d.nodes[entry.ID]
	if !ok {
		return nil, fmt.Errorf("%w: fake read %q", ErrNotFound, entry.ID)
	}
	data := n.data
	if offset > int64(len(data)) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	start := offset
	end := int64(len(data))
	if size >= 0 && offset+size < end {
		end = offset + size
	}
	return io.NopCloser(bytes.NewReader(data[start:end])), nil
}

// Mkdir implements Driver.
func (d *FakeDriver) Mkdir(ctx context.Context, parentID, name string) (Entry, error) {
	d.mu.Lock()
	d.record("Mkdir", parentID+"/"+name)
	d.mu.Unlock()
	if err := d.failOn("Mkdir"); err != nil {
		return Entry{}, err
	}
	if err := d.wait(ctx, d.Delay); err != nil {
		return Entry{}, err
	}
	if err := d.consume(&d.ErrMkdir); err != nil {
		return Entry{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.nodes[parentID]; !ok {
		return Entry{}, fmt.Errorf("%w: fake mkdir parent %q", ErrNotFound, parentID)
	}
	id := parentID + "/" + name
	if _, exists := d.nodes[id]; exists {
		return Entry{}, fmt.Errorf("%w: fake mkdir %q exists", fs.ErrExist, id)
	}
	d.nextID++
	d.nodes[id] = &fakeNode{name: name, isDir: true, parentID: parentID, modTime: time.Now()}
	d.captureLocked()
	return Entry{ID: id, ParentID: parentID, Name: name, IsDir: true, ModTime: time.Now()}, nil
}

// Move implements Driver.
func (d *FakeDriver) Move(ctx context.Context, entry Entry, dstParentID string) error {
	d.mu.Lock()
	d.record("Move", entry.ID)
	d.mu.Unlock()
	if err := d.failOn("Move"); err != nil {
		return err
	}
	if err := d.wait(ctx, d.Delay); err != nil {
		return err
	}
	if err := d.consume(&d.ErrMove); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	n, ok := d.nodes[entry.ID]
	if !ok {
		return fmt.Errorf("%w: fake move %q", ErrNotFound, entry.ID)
	}
	if _, ok := d.nodes[dstParentID]; !ok {
		return fmt.Errorf("%w: fake move destination %q", ErrNotFound, dstParentID)
	}
	d.rekey(entry.ID, n, dstParentID)
	d.captureLocked()
	return nil
}

// Rename implements Driver.
func (d *FakeDriver) Rename(ctx context.Context, entry Entry, newName string) error {
	d.mu.Lock()
	d.record("Rename", entry.ID)
	d.mu.Unlock()
	if err := d.failOn("Rename"); err != nil {
		return err
	}
	if err := d.wait(ctx, d.Delay); err != nil {
		return err
	}
	if err := d.consume(&d.ErrRename); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	n, ok := d.nodes[entry.ID]
	if !ok {
		return fmt.Errorf("%w: fake rename %q", ErrNotFound, entry.ID)
	}
	d.rekey(entry.ID, n, n.parentID)
	d.nodes[entry.ID].name = newName
	d.captureLocked()
	return nil
}

// rekey moves a node (and its subtree) to a new parent, keeping ids
// path-based. Caller must hold d.mu.
func (d *FakeDriver) rekey(oldID string, n *fakeNode, newParentID string) {
	newID := newParentID + "/" + n.name
	if newID == oldID {
		n.parentID = newParentID
		return
	}
	d.nodes[newID] = n
	n.parentID = newParentID
	delete(d.nodes, oldID)
	// Re-key descendants.
	for id, child := range d.nodes {
		if strings.HasPrefix(id, oldID+"/") {
			rel := strings.TrimPrefix(id, oldID)
			childID := newID + rel
			child.parentID = newID + "/" + strings.Trim(strings.TrimPrefix(child.parentID, oldID), "/")
			d.nodes[childID] = child
			delete(d.nodes, id)
		}
	}
}

// Remove implements Driver; missing objects classify as ErrNotFound.
func (d *FakeDriver) Remove(ctx context.Context, entry Entry) error {
	d.mu.Lock()
	d.record("Remove", entry.ID)
	d.mu.Unlock()
	if err := d.failOn("Remove"); err != nil {
		return err
	}
	if err := d.wait(ctx, d.Delay); err != nil {
		return err
	}
	if err := d.consume(&d.ErrRemove); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.nodes[entry.ID]; !ok {
		return fmt.Errorf("%w: fake remove %q", ErrNotFound, entry.ID)
	}
	// Remove the subtree.
	for id := range d.nodes {
		if id == entry.ID || strings.HasPrefix(id, entry.ID+"/") {
			delete(d.nodes, id)
		}
	}
	d.captureLocked()
	return nil
}

// PutSource implements Driver: stores the payload under the request's target
// path (params.path or the default upload path) and returns the new entry.
func (d *FakeDriver) PutSource(ctx context.Context, req UploadRequest) (Entry, error) {
	d.mu.Lock()
	d.record("PutSource", req.Name)
	d.mu.Unlock()
	if err := d.failOn("PutSource"); err != nil {
		return Entry{}, err
	}
	if err := d.wait(ctx, d.Delay+d.PutDelay); err != nil {
		return Entry{}, err
	}
	if err := d.consume(&d.ErrPutSource); err != nil {
		return Entry{}, err
	}
	handle, err := req.Source.Open(ctx)
	if err != nil {
		return Entry{}, err
	}
	payload, err := io.ReadAll(handle)
	_ = handle.Close()
	if err != nil {
		return Entry{}, err
	}
	parentID := req.ParentID
	if parentID == "" {
		parentID = d.rootID
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.nodes[parentID]; !ok {
		return Entry{}, fmt.Errorf("%w: fake put parent %q", ErrNotFound, parentID)
	}
	stamped := time.Now()
	if !req.ModTime.IsZero() {
		stamped = req.ModTime
	}
	id := parentID + "/" + req.Name
	if n, exists := d.nodes[id]; exists {
		n.data = payload
		n.modTime = stamped
		d.captureLocked()
		return Entry{ID: id, ParentID: parentID, Name: req.Name, Size: int64(len(payload)), ModTime: stamped}, nil
	}
	d.nodes[id] = &fakeNode{name: req.Name, parentID: parentID, data: payload, modTime: stamped}
	d.captureLocked()
	return Entry{ID: id, ParentID: parentID, Name: req.Name, Size: int64(len(payload)), ModTime: stamped}, nil
}

// RequiredUploadHashes implements Driver.
func (d *FakeDriver) RequiredUploadHashes() []HashAlgorithm { return nil }

// ResolvePath implements Driver (path-based ids make this identity).
func (d *FakeDriver) ResolvePath(ctx context.Context, path string) (string, error) {
	d.mu.Lock()
	d.record("ResolvePath", path)
	d.mu.Unlock()
	if err := d.failOn("ResolvePath"); err != nil {
		return "", err
	}
	if err := d.wait(ctx, d.Delay); err != nil {
		return "", err
	}
	if err := d.consume(&d.ErrResolve); err != nil {
		return "", err
	}
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return d.rootID, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.nodes[trimmed]; !ok {
		return "", fmt.Errorf("%w: fake resolve %q", ErrNotFound, path)
	}
	return trimmed, nil
}

// ResolveRemoteName implements Driver: identity for plain names.
func (d *FakeDriver) ResolveRemoteName(ctx context.Context, plainName string) (RemoteNameInfo, error) {
	d.mu.Lock()
	d.record("ResolveRemoteName", plainName)
	d.mu.Unlock()
	if err := d.failOn("ResolveRemoteName"); err != nil {
		return RemoteNameInfo{}, err
	}
	if err := d.wait(ctx, d.Delay); err != nil {
		return RemoteNameInfo{}, err
	}
	return RemoteNameInfo{RemoteName: plainName}, nil
}

// ForeignEntries implements Driver.
func (d *FakeDriver) ForeignEntries(ctx context.Context, parentID string) ([]ForeignEntry, error) {
	d.mu.Lock()
	d.record("ForeignEntries", parentID)
	d.mu.Unlock()
	if err := d.failOn("ForeignEntries"); err != nil {
		return nil, err
	}
	if err := d.wait(ctx, d.Delay); err != nil {
		return nil, err
	}
	return nil, nil
}

// Space implements Driver.
func (d *FakeDriver) Space(ctx context.Context) (Space, error) {
	d.mu.Lock()
	d.record("Space", "")
	d.mu.Unlock()
	if err := d.failOn("Space"); err != nil {
		return Space{}, err
	}
	if err := d.wait(ctx, d.Delay); err != nil {
		return Space{}, err
	}
	if err := d.consume(&d.ErrSpace); err != nil {
		return Space{}, err
	}
	if !d.hasSpace {
		return Space{}, ErrSpaceUnsupported
	}
	return d.space, nil
}

// Capabilities implements Driver.
func (d *FakeDriver) Capabilities() []Capability { return d.caps }

// DebugSnapshot implements Driver.
func (d *FakeDriver) DebugSnapshot(ctx context.Context) (DebugSnapshot, error) {
	return DebugSnapshot{}, nil
}

// Metrics implements Driver.
func (d *FakeDriver) Metrics(ctx context.Context, since time.Time) ([]MetricEvent, error) {
	return nil, nil
}

// Ensure *FakeDriver implements Driver.
var _ Driver = (*FakeDriver)(nil)
