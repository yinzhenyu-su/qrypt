# qrypt cgofuse patch

This directory contains `github.com/winfsp/cgofuse` v1.6.0 under its MIT
license. qrypt carries two small extensions:

- `FileSystemHost.SetCapNodeRWLock` allows a thread-safe filesystem to declare
  macFUSE's `FUSE_CAP_NODE_RWLOCK` capability.
- `FileSystemHost.Notify` resolves the optional Unix `fuse_invalidate_path`
  symbol so asynchronous qrypt mutations can invalidate kernel path caches.

Keep changes relative to v1.6.0 limited to these APIs and their wiring so the
fork can be removed when upstream provides equivalent options.
