package upload

import (
	"context"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

const (
	DefaultTargetIndexTTL = 2 * time.Second
	maxTargetIndexParents = 1024
)

// TargetIndex keeps a short-lived, upload-only view of remote parent
// directories. It coalesces concurrent List calls and serializes uploads that
// target the same final name. Successful mutations are folded into the view by
// indexedRemoteOps; failed mutations invalidate it.
type TargetIndex struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	parents map[string]*parentTargetState
	nextID  uint64
}

type parentTargetState struct {
	entries      map[string][]drive.Entry
	loadedAt     time.Time
	loading      *targetIndexLoad
	reservations map[string]uint64
	changed      chan struct{}
	version      uint64
}

type targetIndexLoad struct {
	done    chan struct{}
	version uint64
	err     error
}

type targetLease struct {
	parentID string
	name     string
	id       uint64
}

type targetCacheInfo struct {
	status string
	age    time.Duration
}

func NewTargetIndex(ttl time.Duration) *TargetIndex {
	return &TargetIndex{
		ttl:     ttl,
		now:     time.Now,
		parents: map[string]*parentTargetState{},
	}
}

func (i *TargetIndex) prepare(ctx context.Context, remote RemoteOps, parentID, name, fid, replaceUploadID string) (uploadTarget, targetLease, targetCacheInfo, error) {
	target := uploadTarget{UploadName: name}
	if !remote.CanWrite() {
		return target, targetLease{}, targetCacheInfo{status: "disabled"}, nil
	}

	lease, err := i.reserve(ctx, parentID, name)
	if err != nil {
		return target, targetLease{}, targetCacheInfo{}, err
	}
	release := true
	defer func() {
		if release {
			i.release(lease)
		}
	}()

	entries, info, err := i.list(ctx, remote, parentID)
	if err != nil {
		return target, targetLease{}, info, err
	}
	target, stale := prepareUploadTargetFromEntries(entries, name, fid, replaceUploadID)
	for _, entry := range stale {
		logging.L.InfofEvery("vfs.remove_stale_temp_upload", time.Second, "[VFS] removing stale temporary upload parent=%q name=%q id=%q size=%d", parentID, entry.Name, entry.ID, entry.Size)
		if err := remote.Remove(ctx, entry); err != nil {
			return uploadTarget{}, targetLease{}, info, err
		}
	}
	release = false
	return target, lease, info, nil
}

func (i *TargetIndex) reserve(ctx context.Context, parentID, name string) (targetLease, error) {
	for {
		i.mu.Lock()
		state := i.parentLocked(parentID)
		if _, busy := state.reservations[name]; !busy {
			i.nextID++
			lease := targetLease{parentID: parentID, name: name, id: i.nextID}
			state.reservations[name] = lease.id
			i.mu.Unlock()
			return lease, nil
		}
		changed := state.changed
		i.mu.Unlock()
		select {
		case <-ctx.Done():
			return targetLease{}, ctx.Err()
		case <-changed:
		}
	}
}

func (i *TargetIndex) release(lease targetLease) {
	if lease.id == 0 {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	state := i.parents[lease.parentID]
	if state == nil || state.reservations[lease.name] != lease.id {
		return
	}
	delete(state.reservations, lease.name)
	i.notifyLocked(state)
}

func (i *TargetIndex) list(ctx context.Context, remote RemoteOps, parentID string) ([]drive.Entry, targetCacheInfo, error) {
	shared := false
	for {
		now := i.now()
		i.mu.Lock()
		state := i.parentLocked(parentID)
		if state.entries != nil && i.ttl > 0 && now.Sub(state.loadedAt) < i.ttl {
			entries := flattenTargetEntries(state.entries)
			status := "hit"
			if shared {
				status = "shared"
			}
			info := targetCacheInfo{status: status, age: now.Sub(state.loadedAt)}
			i.mu.Unlock()
			return entries, info, nil
		}
		if state.loading != nil {
			load := state.loading
			i.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, targetCacheInfo{status: "shared"}, ctx.Err()
			case <-load.done:
			}
			if load.err != nil {
				return nil, targetCacheInfo{status: "shared"}, load.err
			}
			shared = true
			continue
		}
		load := &targetIndexLoad{done: make(chan struct{}), version: state.version}
		state.loading = load
		i.mu.Unlock()

		entries, err := remote.List(ctx, parentID)
		loadedAt := i.now()
		i.mu.Lock()
		state = i.parentLocked(parentID)
		if state.loading == load {
			state.loading = nil
			load.err = err
			if err != nil {
				state.entries = nil
				state.loadedAt = time.Time{}
			} else if state.version == load.version {
				state.entries = indexTargetEntries(entries)
				state.loadedAt = loadedAt
			}
			close(load.done)
		}
		versionChanged := state.version != load.version
		retry := versionChanged && state.entries == nil
		if err == nil && versionChanged && state.entries != nil {
			entries = flattenTargetEntries(state.entries)
		}
		i.mu.Unlock()
		if err != nil {
			return nil, targetCacheInfo{status: "miss"}, err
		}
		if retry {
			shared = true
			continue
		}
		return entries, targetCacheInfo{status: "miss"}, nil
	}
}

func (i *TargetIndex) upsert(entry drive.Entry) {
	if entry.ParentID == "" || entry.ID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	state := i.parents[entry.ParentID]
	if state == nil || state.entries == nil {
		return
	}
	removeTargetEntryByID(state.entries, entry.ID)
	state.entries[entry.Name] = append(state.entries[entry.Name], entry)
	state.loadedAt = i.now()
	state.version++
}

func (i *TargetIndex) remove(entry drive.Entry) {
	if entry.ParentID == "" || entry.ID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	state := i.parents[entry.ParentID]
	if state == nil || state.entries == nil {
		return
	}
	removeTargetEntryByID(state.entries, entry.ID)
	state.loadedAt = i.now()
	state.version++
}

func (i *TargetIndex) rename(entry drive.Entry, newName string) {
	entry.Name = newName
	i.upsert(entry)
}

func (i *TargetIndex) invalidate(parentID string) {
	if parentID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	state := i.parentLocked(parentID)
	state.entries = nil
	state.loadedAt = time.Time{}
	state.version++
}

func (i *TargetIndex) parentLocked(parentID string) *parentTargetState {
	state := i.parents[parentID]
	if state == nil {
		if len(i.parents) >= maxTargetIndexParents {
			i.pruneLocked()
		}
		state = &parentTargetState{
			reservations: map[string]uint64{},
			changed:      make(chan struct{}),
		}
		i.parents[parentID] = state
	}
	return state
}

func (i *TargetIndex) pruneLocked() {
	now := i.now()
	for parentID, state := range i.parents {
		if state.loading != nil || len(state.reservations) > 0 {
			continue
		}
		if state.entries != nil && i.ttl > 0 && now.Sub(state.loadedAt) < i.ttl {
			continue
		}
		delete(i.parents, parentID)
		if len(i.parents) < maxTargetIndexParents {
			return
		}
	}
}

func (i *TargetIndex) notifyLocked(state *parentTargetState) {
	close(state.changed)
	state.changed = make(chan struct{})
}

func indexTargetEntries(entries []drive.Entry) map[string][]drive.Entry {
	indexed := make(map[string][]drive.Entry, len(entries))
	for _, entry := range entries {
		indexed[entry.Name] = append(indexed[entry.Name], entry)
	}
	return indexed
}

func flattenTargetEntries(indexed map[string][]drive.Entry) []drive.Entry {
	var entries []drive.Entry
	for _, named := range indexed {
		entries = append(entries, named...)
	}
	return entries
}

func removeTargetEntryByID(indexed map[string][]drive.Entry, id string) {
	for name, entries := range indexed {
		kept := entries[:0]
		for _, entry := range entries {
			if entry.ID != id {
				kept = append(kept, entry)
			}
		}
		if len(kept) == 0 {
			delete(indexed, name)
		} else {
			indexed[name] = kept
		}
	}
}

type indexedRemoteOps struct {
	RemoteOps
	index *TargetIndex
}

func (r indexedRemoteOps) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	entry, err := r.RemoteOps.PutSource(ctx, req)
	if err != nil {
		r.index.invalidate(req.ParentID)
		return entry, err
	}
	r.index.upsert(entry)
	return entry, nil
}

func (r indexedRemoteOps) Remove(ctx context.Context, entry drive.Entry) error {
	if err := r.RemoteOps.Remove(ctx, entry); err != nil {
		r.index.invalidate(entry.ParentID)
		return err
	}
	r.index.remove(entry)
	return nil
}

func (r indexedRemoteOps) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	if err := r.RemoteOps.Rename(ctx, entry, newName); err != nil {
		r.index.invalidate(entry.ParentID)
		return err
	}
	r.index.rename(entry, newName)
	return nil
}
