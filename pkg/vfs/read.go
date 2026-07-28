package vfs

import (
	"bytes"
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
	"io"
)

type debugReadCloser struct {
	io.ReadCloser
	finish func(int64, error)
}

func (v *VFS) Read(ctx context.Context, path string, offset, size int64) (rc io.ReadCloser, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpRead, err) }()
	path = cleanVirtual(path)
	started := timeutil.Now()
	opID := v.nextDebugReadOpID()
	activeID := v.beginDebugActive(DebugActiveOp{
		OpID:      opID,
		Kind:      "vfs_read",
		Phase:     "resolve",
		Path:      path,
		Offset:    offset,
		Requested: size,
	})
	if pending, err := v.pendingUpload(path); err == nil {
		v.updateDebugActive(activeID, func(op *DebugActiveOp) {
			op.Phase = "staging_flush"
			op.RemoteID = pending.FID
		})
		if err := newVFSReadRuntime(v).FlushStaging(pending.LocalPath); err != nil {
			v.finishDebugActive(activeID)
			v.recordDebugRead(opID, path, pending.FID, offset, size, 0, "staging", 0, 0, 0, started, nil, err)
			return nil, err
		}
		v.updateDebugActive(activeID, func(op *DebugActiveOp) {
			op.Phase = "staging_open"
		})
		rc, err := osutil.OpenRead(pending.LocalPath, offset, size)
		if err != nil {
			v.finishDebugActive(activeID)
			v.recordDebugRead(opID, path, pending.FID, offset, size, 0, "staging", 0, 0, 0, started, nil, err)
			return nil, err
		}
		return &debugReadCloser{ReadCloser: rc, finish: func(bytes int64, readErr error) {
			v.finishDebugActive(activeID)
			v.recordDebugRead(opID, path, pending.FID, offset, size, bytes, "staging", 0, 0, 0, started, nil, readErr)
		}}, nil
	}
	entry, err := v.resolve(ctx, path)
	if err != nil {
		v.finishDebugActive(activeID)
		v.recordDebugRead(opID, path, "", offset, size, 0, "remote", 0, 0, 0, started, nil, err)
		return nil, err
	}
	if entry.IsDir {
		err := fmt.Errorf("vfs: %s is a directory", path)
		v.finishDebugActive(activeID)
		v.recordDebugRead(opID, path, entry.ID, offset, size, 0, "remote", 0, 0, 0, started, nil, err)
		return nil, err
	}
	v.updateDebugActive(activeID, func(op *DebugActiveOp) {
		op.Phase = "read_range"
		op.RemoteID = entry.ID
	})
	hitsBefore, missesBefore := v.debugCacheCounters()
	readCtx := drive.WithDebugOperation(ctx, drive.DebugOperation{OpID: opID, Step: "vfs_read", Name: path})
	windowChunks := readWindowChunks(size)
	data, startChunk, endChunk, err := v.readRange(readCtx, entry, offset, size, windowChunks)
	hitsAfter, missesAfter := v.debugCacheCounters()
	if err != nil {
		v.finishDebugActive(activeID)
		v.recordDebugRead(opID, path, entry.ID, offset, size, 0, "remote", hitsAfter-hitsBefore, missesAfter-missesBefore, 0, started, readWindowExtra(windowChunks), err)
		return nil, err
	}
	if readPrefetchEnabled(ctx) {
		v.updateDebugActive(activeID, func(op *DebugActiveOp) {
			op.Phase = "prefetch_schedule"
			op.Extra = map[string]any{
				"start_chunk":   startChunk,
				"end_chunk":     endChunk,
				"window_chunks": windowChunks,
			}
		})
		v.prefetchAdjacentChunks(readCtx, entry, endChunk, windowChunks)
	}
	var chunks int64
	if len(data) > 0 {
		chunks = endChunk - startChunk + 1
	}
	v.finishDebugActive(activeID)
	v.recordDebugRead(opID, path, entry.ID, offset, size, int64(len(data)), "remote", hitsAfter-hitsBefore, missesAfter-missesBefore, chunks, started, readWindowExtra(windowChunks), nil)
	return io.NopCloser(bytes.NewReader(data)), nil
}

const readChunkSize = 1024 * 1024
const readPrefetchLimit = 2
const readPrefetchChunks = 8

// readMaxConcurrency caps total concurrent remote read windows.
const readMaxConcurrency = 8

// readHighReserve is the number of read slots reserved for high-priority reads.
const readHighReserve = 2
const readHotChunkLimit = 16
const readRangeHitLimit = 1024
const readRangePromoteHits = 2

func (v *VFS) acquireReadSlot(ctx context.Context) (func(), error) {
	return newVFSReadRuntime(v).AcquireSlot(ctx)
}
