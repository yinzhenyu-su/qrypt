# qrypt cgofuse patch

This directory contains `github.com/winfsp/cgofuse` v1.6.0 under its MIT
license. qrypt carries one small extension: `FileSystemHost.SetCapNodeRWLock`
allows a thread-safe filesystem to declare macFUSE's `FUSE_CAP_NODE_RWLOCK`
capability. Upstream cgofuse does not currently expose that capability.

Keep changes relative to v1.6.0 limited to this API and its capability wiring
so the fork can be removed when upstream provides an equivalent option.
