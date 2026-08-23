# Developer FAQ

This page records recurring implementation issues and the reasoning behind the
current fixes. It is meant to help contributors find the right layer to inspect
before making changes.

## Finder copy fails when `ignore_apple_metadata = true`

### Symptom

Original report:

> If a directory contains a `.DS_Store` file, dragging that directory from
> Finder into the mounted drive may show "无法完成此操作" ("The operation can't be
> completed"). This is a regression.

On macOS, copying a directory with Finder may fail with a generic message such
as "The operation can't be completed" when:

```toml
ignore_apple_metadata = true
```

The same copy may work when:

```toml
ignore_apple_metadata = false
```

Changing `delegate_apple_xattr` may not affect this failure.

### Why This Happens

Finder does more than copy regular files. During a directory copy it may create,
probe, update, read, and remove Apple metadata paths such as:

- `.DS_Store`
- `._filename`
- `.Spotlight-V100`
- `.fseventsd`

When `ignore_apple_metadata = true`, qrypt should hide or ignore those metadata files
without changing the behavior Finder expects from the filesystem.

A subtle failure mode is returning "exists" for an AppleDouble path that Finder
only probed and never created. For example, if `Getattr("/dir/._entry.js")`
returns success just because the name matches `._*`, Finder can take a different
copy path and later clean up or abort the directory copy.

The correct behavior is:

- Write-like operations for Apple metadata may create an in-memory ignored node.
- Lookup/read-like operations should report the node only if it was actually
  created during this mount process.
- Missing AppleDouble paths should still return `ENOENT`.

### What To Check

Enable debug logs and reproduce the copy:

```toml
[logging]
log_level = "debug"
log_file = "~/.qrypt/qrypt.log"
error_file = "~/.qrypt/qrypt-error.log"
```

Look for FUSE operations around the copied directory:

```sh
rg 'Getattr|Create|Open|Write|Flush|Unlink|Rmdir|Setxattr|Getxattr' ~/.qrypt/qrypt.log
```

Important signs:

- `Getattr` for a missing `._*` path should return `ENOENT`.
- `.DS_Store` and `._*` writes should not reach the remote driver when
  `ignore_apple_metadata = true`.
- Finder may remove and recreate the destination directory after an earlier
  failure. That can be a symptom, not the root cause.
- If `ignore_apple_metadata = false` works, compare whether the failure is specific to
  the ignored Apple metadata path.

### About `delegate_apple_xattr`

`delegate_apple_xattr` controls whether Apple-specific extended attributes
such as:

- `com.apple.FinderInfo`
- `com.apple.quarantine`
- `com.apple.metadata:*`

It is separate from `ignore_apple_metadata`.

When `delegate_apple_xattr = true`, qrypt returns `ENOTSUP` for those xattrs so
macFUSE can handle them locally through AppleDouble. When
`delegate_apple_xattr = false`, qrypt keeps xattrs in memory for Finder
compatibility. In either case, they are not uploaded to the remote drive.

### Regression Tests

Keep tests focused on filesystem semantics rather than one Finder version.
Relevant tests live in `pkg/mount/mount_test.go` and should cover:

- Missing AppleDouble lookup returns `ENOENT`.
- Ignored `.DS_Store` supports write, read, offset write, truncate, and rename
  during the same mount process.
- Ignored nested metadata files still create the real parent directory when
  needed.
- Ignored metadata time updates do not touch the backend.
- xattrs can be set, read, listed, removed, renamed, and removed with their
  subtree when `delegate_apple_xattr = false`.

Run:

```sh
go test ./pkg/mount
go test ./...
```

### Practical Guidance

When fixing Finder copy issues, avoid solving them only by checking file names.
Finder depends on a sequence of FUSE semantics. A path should not appear to
exist unless qrypt has a real backend entry or an in-memory node created by an
earlier operation in the same mount process.

### macOS 26.6.1 regression

macOS 26.6.1 后，Finder 在 qrypt 挂载点复制文件报 EEXIST/-48。它在创建空文件、处理扩展属性后中止并删除，尚未执行 Write/Upload，与 upload_delay 无关。旧版 qrypt 在新系统失败且 macFUSE 未更新，表明是系统兼容性变化。Darwin 启用 auto_xattr 可绕过故障路径，AppleDouble 仍由 qrypt 控制。

## Why cross-drive copies write more data to disk than expected

> **TL;DR**: FUSE does not let the daemon distinguish a copy read from a
> regular read, so the read-cache policy cannot decide between "cache
> everything" (copies amplify the write 1x) and "cache nothing" (playback and
> thumbnail reuse lose their hits). qrypt routes explicit copies (`fs copy`,
> copy tasks) around the blind spot via the driver-level path; the remaining
> amplification is only unlabelled FUSE `cp` and cross-drive `fs sync`.

### The two copy paths and their amplification

Copying a file from one netdisk mount to another can go through two very
different code paths. The total local disk writes (as a multiple of file
size `S`) differ by 2-3x:

| Copy path | staging | read cache | cache seed | total |
|---|---|---|---|---|
| `qrypt fs copy` (direct copy, `control.RunDirectDriverCopy`) | 0 | 0 | 0 | **1xS** (one temp file) |
| FUSE `cp` / cross-drive `fs sync` (`fs.WriteAt`) | 1xS | 0-1xS | 0-1xS | **2-3xS** |

### Where the extra writes come from

1. **Direct copy still writes one temp file** (`copySourceToTemp` in
   `pkg/control/directcopy.go`). The whole source is streamed into an
   `os.CreateTemp` file (with `tmp.Sync()`) while md5/sha1/sha256 hashes are
   computed through an `io.MultiWriter`, then uploaded via `PutSource` and
   deleted. The temp file exists so a retried upload does not re-read the
   source netdisk; the hashes require one sequential pass. This is the
   unavoidable 1x.

2. **FUSE write path writes the staging file** (`staging_write.go`). Every
   `WriteAt` goes into a local staging file before the upload worker streams
   it to the destination driver. This 1x is inherent to the
   write-then-upload model.

3. **Reading the source may write the read cache** (`read_window.go`
   `StoreChunk` -> `PutChunkAsync`). Unlike cache seeding, the read path has
   **no large-file skip**: every fetched chunk is persisted even though a
   one-shot copy will never reuse it. Eviction later reclaims large entries
   (`read_cache_eviction.go`), but the write has already happened. This is
   pure amplification for a copy.

4. **Cache seeding writes the staging file again** (`upload_snapshot.go`
   `seedReadCacheFromStaging` -> `PutLocalFile`). After a successful upload
   the staging file is copied into cache batch files. Files at or above
   `readCacheLargeFileBytes` (16 MiB) are skipped, so this is at most 1x for
   small files.

5. **Upload journal appends** (`upload_journal.go`) write a small record per
   pending-state transition (created / flushed / failed / committed) and are
   periodically compacted without fsync. This is negligible in bytes but is
   the reason the pending count grows during a copy.

### What is already avoided

- **No read-modify-write on staging**: staging is a standalone file written
  with `pwrite` at arbitrary offsets, so unaligned application writes never
  trigger a block-level read-modify-write.
- **No second staging copy**: the upload streams directly from the staging
  file; it is not copied into an upload buffer.
- **Large files skip cache seeding** (16 MiB threshold).
- **Journal compaction** keeps the journal file bounded.

### Why FUSE cannot detect cross-drive copies

A `cp` between two qrypt mounts is, from the FUSE daemon's point of view, a
plain `open`/`read`/`write`/`flush` sequence. FUSE has no copy operation, so
the daemon cannot tell that the writes to the destination originated from a
read of the source.

The only kernel-level copy signal is `FUSE_COPY_FILE_RANGE` (Linux fuse >=
3.8), and it has three practical caveats:

- macFUSE (macOS) does not support it, so copies on macOS always degrade to
  read+write.
- qrypt does not implement a `CopyFileRange` handler, so even on Linux the
  kernel falls back to read+write.
- qrypt mounts all drives under one FUSE mount point (a `vfs.Namespace` with
  `/quark`, `/yun139`, ... as path segments), so a cross-drive copy is
  actually a same-mount-point copy; if a `CopyFileRange` handler existed, the
  kernel would deliver it and qrypt could resolve source/destination paths
  and route cross-mount copies to the direct-copy path.

Heuristics (sequential full read + matching full write + same pid) can flag
"likely copy" for diagnostics, but they cannot prevent the amplification (the
writes already happened) and they misclassify sequential readers such as
media players. Do not use them to switch behavior.

### Explicit copy paths and future optimizations

qrypt already routes explicit copies around the amplification: `qrypt fs
copy` and the copy task (`pkg/core/copy.go`) call `RunDirectDriverCopy`, the
driver-level path, regardless of which mounts are involved. The remaining
2-3x path is only for unlabelled FUSE `cp` and for cross-drive `fs sync`
(`pkg/syncer/transfer.go` uses `fs.WriteAt`). Future work, in decreasing
order of benefit:

1. Stream `fs sync` cross-drive transfers through `RunDirectDriverCopy`
   instead of `WriteAt` (2-3x -> 1x).
2. Skip read-cache writes for files at or above `readCacheLargeFileBytes` on
   the read path, matching the seeding threshold.
3. On Linux only, implement `CopyFileRange` and route cross-mount copies to
   the direct-copy path, making FUSE `cp` hit 1x instead of 2-3x.
