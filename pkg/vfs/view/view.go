// Package view owns the VFS local tree domain: the entry/list cache that
// mirrors the remote tree, the visibility overlay (deleted / rename / copy
// hiding) and the scheduled-delete tasks. The three state structs are the
// mutation target of every local write; the composite Overlay+Tasks pair
// deliberately shares one mutex (see NewOverlayTasks) so cross-domain
// transitions - a delete commit marking the overlay while unscheduling the
// timer - stay atomic.
//
// Package vfs is the assembly layer: it owns the VFS struct, builds these
// states, and implements the engine adapters (read/listing/delete/mutation/
// upload/diagnostics) over them. Nothing in this package imports pkg/vfs.
package view

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/scheduler"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// ErrNotFound aliases the drive not-found sentinel (also aliased by pkg/vfs),
// so errors.Is comparisons work across the package boundary.
var ErrNotFound = drive.ErrNotFound

// listCacheEntry is one cached directory listing with its expiry.
type listCacheEntry struct {
	entries []drive.Entry
	expires time.Time
}

// CloneEntries returns a copy of a listing slice so callers can mutate
// entries in place without surprising the slice owner.
func CloneEntries(entries []drive.Entry) []drive.Entry {
	if entries == nil {
		return nil
	}
	out := make([]drive.Entry, len(entries))
	copy(out, entries)
	return out
}

// IsAppleMetadataName reports whether name is Apple metadata (a .DS_Store
// file or an AppleDouble ._ sidecar) that directory copies should never
// surface.
func IsAppleMetadataName(name string) bool {
	return name == ".DS_Store" || strings.HasPrefix(name, "._")
}

const deleteTaskPrefix = "delete:"

func deleteTaskID(entry drive.Entry, p string) string {
	if entry.ID != "" {
		return deleteTaskPrefix + entry.ID
	}
	return deleteTaskPrefix + strings.TrimPrefix(vfstypes.CleanVirtualPath(p), "/")
}

// entryListHasPath reports whether entries contains name, optionally
// requiring a specific remote ID.
func entryListHasPath(entries []drive.Entry, name, entryID string) bool {
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		if entryID == "" || entry.ID == "" || entry.ID == entryID {
			return true
		}
	}
	return false
}

const (
	// LocalCreateLookupTTL is how long a locally created directory shadows
	// remote lookups, so a freshly created dir resolves locally before the
	// remote drive has caught up.
	LocalCreateLookupTTL = 2 * time.Minute
	// RestoredDirTTL is how long a restored directory keeps marking its
	// descendants as under-restored.
	RestoredDirTTL = 60 * time.Second
	// DirectoryCopyHideTTL hides a directory's children after a copy so the
	// half-populated remote state is not surfaced while it fills in.
	DirectoryCopyHideTTL = 10 * time.Minute
)

// View is the local mirror of the remote tree: resolved entries, list caches,
// and locally authored state (recent local dirs, local modtimes). Mutated by
// the view runtimes (Runtime, Visibility, Committer, Resolve), never directly
// by pkg/vfs.
type View struct {
	mu           sync.RWMutex
	entries      *vfstypes.ShardedEntryMap
	lists        map[string]listCacheEntry
	localDirs    map[string]time.Time
	localModTime map[string]time.Time
	overlay      *Overlay
}

// NewView builds the view around the tree root. overlay is the paired
// visibility state created by NewOverlayTasks; the caller owns the pairing.
func NewView(rootID string, now time.Time, overlay *Overlay) *View {
	view := &View{
		entries:      vfstypes.NewShardedEntryMap(),
		lists:        map[string]listCacheEntry{},
		localDirs:    map[string]time.Time{},
		localModTime: map[string]time.Time{},
		overlay:      overlay,
	}
	view.entries.Set("/", drive.Entry{ID: rootID, Name: "/", IsDir: true, ModTime: now, CreatedAt: now, UpdatedAt: now})
	return view
}

// Overlay returns the visibility state paired with this view.
func (v *View) Overlay() *Overlay { return v.overlay }

// CachedEntry returns the cached entry identity for path. A miss does not
// mean the path is unavailable - visibility is the overlay's concern.
func (v *View) CachedEntry(path string) (drive.Entry, bool) {
	return v.entries.Get(vfstypes.CleanVirtualPath(path))
}

// RangeEntries visits every cached entry under the view lock.
func (v *View) RangeEntries(fn func(path string, entry drive.Entry) bool) {
	v.entries.Range(fn)
}

// ClearListCaches drops every cached directory listing. Used by callers that
// need to force a fresh remote fetch (tests, prefetch recovery).
func (v *View) ClearListCaches() {
	v.mu.Lock()
	clear(v.lists)
	v.mu.Unlock()
}

// Overlay is the visibility overlay: deleted entries, rename overlays,
// restored-dir markers and copy-hidden children. Overlay and Tasks share one
// mutex (see NewOverlayTasks); Overlay methods that touch Tasks document the
// Locked suffix convention and never acquire the shared lock themselves when
// the caller holds it.
type Overlay struct {
	mu                 *sync.Mutex
	deleted            map[string]drive.Entry
	renameOverlays     map[string]overlayOp
	restoredDirs       map[string]time.Time
	copyHiddenChildren map[string]map[string]time.Time
	// deletedDirs indexes the directory entries of the deleted map so
	// IsDeleted can walk the O(depth) ancestor chain instead of scanning
	// every overlay entry. renameHiddenDirs does the same for recursive
	// rename overlays (keyed by oldPath). Both are maintained by the
	// set/remove helpers below, always with mu held.
	deletedDirs      map[string]struct{}
	renameHiddenDirs map[string]struct{}
}

// Tasks is the scheduled-delete domain state: active deletes, recorded
// failures, directory takeovers and the timer scheduler. It shares the
// Overlay mutex so delete-commit transitions are atomic.
type Tasks struct {
	mu        *sync.Mutex
	scheduler scheduler.KeyedScheduler
	active    map[string]drive.Entry
	failures  map[string]string
	takeovers map[string]struct{}
	changed   chan struct{}
}

// NewOverlayTasks builds the Overlay+Tasks composite sharing one mutex. The
// pair must stay together: delete commits write both under the single lock.
func NewOverlayTasks() (*Overlay, *Tasks) {
	mu := &sync.Mutex{}
	return &Overlay{
		mu:                 mu,
		deleted:            map[string]drive.Entry{},
		renameOverlays:     map[string]overlayOp{},
		restoredDirs:       map[string]time.Time{},
		copyHiddenChildren: map[string]map[string]time.Time{},
		deletedDirs:        map[string]struct{}{},
		renameHiddenDirs:   map[string]struct{}{},
	}, &Tasks{
		mu:        mu,
		scheduler: scheduler.NewTimeKeyedScheduler(),
		active:    map[string]drive.Entry{},
		failures:  map[string]string{},
		takeovers: map[string]struct{}{},
		changed:   make(chan struct{}),
	}
}

// --- overlay map maintenance helpers (callers must hold o.mu) ---

func (o *Overlay) setDeleted(path string, entry drive.Entry) {
	o.deleted[path] = entry
	if entry.IsDir {
		o.deletedDirs[path] = struct{}{}
	} else {
		delete(o.deletedDirs, path)
	}
}

func (o *Overlay) removeDeleted(path string) {
	delete(o.deleted, path)
	delete(o.deletedDirs, path)
}

func (o *Overlay) setRenameOverlay(op overlayOp) {
	o.renameOverlays[op.oldPath] = op
	if op.isDir {
		o.renameHiddenDirs[op.oldPath] = struct{}{}
	} else {
		delete(o.renameHiddenDirs, op.oldPath)
	}
}

func (o *Overlay) removeRenameOverlay(path string) {
	delete(o.renameOverlays, path)
	delete(o.renameHiddenDirs, path)
}

// isDeletedPath reports whether path itself is deleted or lives under a
// deleted directory. Exact hits are O(1); directory shadowing walks the
// ancestor chain (O(depth)) instead of scanning every overlay entry.
func (o *Overlay) isDeletedPath(path string) bool {
	if _, ok := o.deleted[path]; ok {
		return true
	}
	for dir := parentVirtualPath(path); ; dir = parentVirtualPath(dir) {
		if _, ok := o.deletedDirs[dir]; ok {
			return true
		}
		if dir == "/" || dir == "." {
			return false
		}
	}
}

func (o *Overlay) isRenameHiddenPath(path string) bool {
	if _, ok := o.renameOverlays[path]; ok {
		return true
	}
	for dir := parentVirtualPath(path); ; dir = parentVirtualPath(dir) {
		if _, ok := o.renameHiddenDirs[dir]; ok {
			return true
		}
		if dir == "/" || dir == "." {
			return false
		}
	}
}

// deepestDeletedAncestor returns the deepest deleted directory at or above
// path (matching the longest-match semantics of the former full scan).
func (o *Overlay) deepestDeletedAncestor(path string) (string, drive.Entry, bool) {
	for dir := path; ; dir = parentVirtualPath(dir) {
		if entry, ok := o.deleted[dir]; ok && entry.IsDir {
			return dir, entry, true
		}
		if dir == "/" || dir == "." {
			return "", drive.Entry{}, false
		}
	}
}

// overlayOp is a rename shadow: while a remote rename is in flight, listings
// hide the old path until it disappears and the new path appears.
type overlayOp struct {
	oldPath string
	newPath string
	entryID string
	isDir   bool
	oldGone bool
	newSeen bool
}

// parentVirtualPath returns the parent of a slash-absolute virtual path
// without allocation (callers guarantee vfstypes-clean input).
func parentVirtualPath(path string) string {
	if path == "/" {
		return "/"
	}
	if i := strings.LastIndexByte(path, '/'); i > 0 {
		return path[:i]
	}
	return "/"
}

// unavailableLocked reports whether path is hidden by any overlay facet:
// deleted, rename-shadowed, or copy-hidden. It never touches the Tasks state.
// Callers hold mu.
func (o *Overlay) unavailableLocked(path string) bool {
	path = vfstypes.CleanVirtualPath(path)
	if o.isDeletedPath(path) || o.isRenameHiddenPath(path) {
		return true
	}
	parentPath := filepath.Dir(path)
	name := filepath.Base(path)
	now := time.Now()
	names := o.copyHiddenChildren[parentPath]
	if len(names) == 0 {
		delete(o.copyHiddenChildren, parentPath)
		return false
	}
	expires, ok := names[name]
	if !ok {
		return false
	}
	if now.After(expires) {
		delete(names, name)
		if len(names) == 0 {
			delete(o.copyHiddenChildren, parentPath)
		}
		return false
	}
	return true
}

// --- Tasks helpers (callers hold the shared mu unless Locked-free) ---

// clearFailureLocked drops a recorded failure for path. Callers hold the
// delete-domain lock (shared with the overlay state, see NewOverlayTasks).
func (s *Tasks) clearFailureLocked(path string) {
	delete(s.failures, path)
}

// unscheduleLocked cancels a pending delete timer for path, reporting whether
// one existed. Callers hold the delete-domain lock.
func (s *Tasks) unscheduleLocked(path string) bool {
	if _, ok := s.scheduler.Keys()[path]; ok {
		s.scheduler.Cancel(path)
		return true
	}
	return false
}

func (s *Tasks) notifyChangedLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Tasks) clearTakeoverLocked(path string) {
	changed := false
	for takeover := range s.takeovers {
		if takeover == path || vfstypes.IsPathUnder(takeover, path) {
			delete(s.takeovers, takeover)
			changed = true
		}
	}
	if changed {
		s.notifyChangedLocked()
	}
}

// SetRestoredDir marks path as a restored directory until expiry. The restore
// operations call this with RestoredDirTTL under their lock; external callers
// (diagnostics seeding, tests) may pick their own expiry.
func (o *Overlay) SetRestoredDir(path string, expiry time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if expiry.After(time.Now()) {
		o.restoredDirs[path] = expiry
	} else {
		delete(o.restoredDirs, path)
	}
}

// StopAll stops every pending delete timer so no delayed delete fires after
// shutdown. Used by the VFS lifecycle.
func (s *Tasks) StopAll() {
	s.scheduler.CancelAll()
}

// Schedule arms or moves a pending delete timer for path.
func (s *Tasks) Schedule(path string, delay time.Duration, fire func()) {
	s.scheduler.Schedule(path, delay, fire)
}

// SetActive marks path as an in-flight delete.
func (s *Tasks) SetActive(path string, entry drive.Entry) {
	s.mu.Lock()
	s.active[path] = entry
	s.clearFailureLocked(path)
	s.notifyChangedLocked()
	s.mu.Unlock()
}

// SetFailure records a failed delete for path.
func (s *Tasks) SetFailure(path, errText string) {
	s.mu.Lock()
	delete(s.active, path)
	if errText != "" {
		s.failures[path] = errText
	}
	s.notifyChangedLocked()
	s.mu.Unlock()
}

// ClearFailure drops any recorded failure for path.
func (s *Tasks) ClearFailure(path string) {
	s.mu.Lock()
	delete(s.failures, path)
	s.mu.Unlock()
}

// Failure returns the recorded failure text for path, if any.
func (s *Tasks) Failure(path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text, ok := s.failures[path]
	return text, ok
}

// Scheduled reports whether path has a pending delete timer.
func (s *Tasks) Scheduled(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.scheduler.Keys()[path]
	return ok
}

// ScheduledPaths returns every path with a pending delete timer.
func (s *Tasks) ScheduledPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := s.scheduler.Keys()
	paths := make([]string, 0, len(keys))
	for path := range keys {
		paths = append(paths, path)
	}
	return paths
}

// WaitActiveChildren blocks until no active delete is pending under dir
// (dir itself excluded) or ctx is cancelled. The waiter re-checks after every
// notify, so a delete starting while another finishes is observed.
func (s *Tasks) WaitActiveChildren(ctx context.Context, dir string) error {
	for {
		s.mu.Lock()
		active := false
		for path := range s.active {
			if path != dir && vfstypes.IsPathUnder(path, dir) {
				active = true
				break
			}
		}
		changed := s.changed
		s.mu.Unlock()
		if !active {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// --- snapshot (diagnostics + tests) ---

// Timer is a path plus an optional deadline.
type Timer struct {
	Path     string
	Deadline time.Time
}

// DeletedEntry is a deleted overlay entry.
type DeletedEntry struct {
	Path  string
	ID    string
	Name  string
	IsDir bool
	Size  int64
}

// RenameOp is a rename overlay operation.
type RenameOp struct {
	OldPath string
	NewPath string
	EntryID string
	IsDir   bool
	OldGone bool
	NewSeen bool
}

// CopyHiddenDir is a directory whose children are hidden until their
// deadlines expire.
type CopyHiddenDir struct {
	Dir   string
	Names []Timer
}

// OverlaySnapshot is the raw composite state under one lock: delete timers,
// deleted entries, rename overlays, restored dirs and copy-hidden children.
type OverlaySnapshot struct {
	DeleteTimers []string
	Deleted      []DeletedEntry
	RenameOps    []RenameOp
	RestoredDirs []Timer
	CopyHidden   []CopyHiddenDir
}

// Snapshot reads the composite state in one critical section. The slices
// come back deterministically sorted so consumers (diagnostics, tests) never
// see map-iteration order.
func (o *Overlay) Snapshot(t *Tasks) OverlaySnapshot {
	now := time.Now()
	o.mu.Lock()
	defer o.mu.Unlock()
	out := OverlaySnapshot{}
	for path := range t.scheduler.Keys() {
		out.DeleteTimers = append(out.DeleteTimers, path)
	}
	for path, entry := range o.deleted {
		out.Deleted = append(out.Deleted, DeletedEntry{
			Path: path, ID: entry.ID, Name: entry.Name, IsDir: entry.IsDir, Size: entry.Size,
		})
	}
	for _, op := range o.renameOverlays {
		out.RenameOps = append(out.RenameOps, RenameOp{
			OldPath: op.oldPath, NewPath: op.newPath, EntryID: op.entryID, IsDir: op.isDir, OldGone: op.oldGone, NewSeen: op.newSeen,
		})
	}
	for path, deadline := range o.restoredDirs {
		if now.After(deadline) {
			continue
		}
		out.RestoredDirs = append(out.RestoredDirs, Timer{Path: path, Deadline: deadline})
	}
	for dir, names := range o.copyHiddenChildren {
		item := CopyHiddenDir{Dir: dir}
		for name, deadline := range names {
			if now.After(deadline) {
				continue
			}
			item.Names = append(item.Names, Timer{Path: name, Deadline: deadline})
		}
		if len(item.Names) > 0 {
			out.CopyHidden = append(out.CopyHidden, item)
		}
	}
	sort.Strings(out.DeleteTimers)
	sort.Slice(out.Deleted, func(i, j int) bool { return out.Deleted[i].Path < out.Deleted[j].Path })
	sort.Slice(out.RenameOps, func(i, j int) bool { return out.RenameOps[i].OldPath < out.RenameOps[j].OldPath })
	sort.Slice(out.RestoredDirs, func(i, j int) bool { return out.RestoredDirs[i].Path < out.RestoredDirs[j].Path })
	sort.Slice(out.CopyHidden, func(i, j int) bool { return out.CopyHidden[i].Dir < out.CopyHidden[j].Dir })
	for i := range out.CopyHidden {
		sort.Slice(out.CopyHidden[i].Names, func(a, b int) bool { return out.CopyHidden[i].Names[a].Path < out.CopyHidden[i].Names[b].Path })
	}
	return out
}

// DeleteRecord is one path's delete-task projection: state flags plus the
// underlying overlay entry. pkg/vfs maps it onto its task API.
type DeleteRecord struct {
	ID        string
	Path      string
	Entry     drive.Entry
	Running   bool
	Scheduled bool
	ErrorText string
	UpdatedAt time.Time
}

// DeleteTaskRecords projects the deleted overlay into delete-task records in
// one critical section (state flags come from the shared Tasks state).
func (r Visibility) DeleteTaskRecords() []DeleteRecord {
	now := util.Now()
	r.overlay.mu.Lock()
	defer r.overlay.mu.Unlock()
	records := make([]DeleteRecord, 0, len(r.overlay.deleted))
	for p, entry := range r.overlay.deleted {
		_, running := r.tasks.active[p]
		_, scheduled := r.tasks.scheduler.Keys()[p]
		records = append(records, DeleteRecord{
			ID:        deleteTaskID(entry, p),
			Path:      p,
			Entry:     entry,
			Running:   running,
			Scheduled: scheduled,
			ErrorText: r.tasks.failures[p],
			UpdatedAt: now,
		})
	}
	return records
}
