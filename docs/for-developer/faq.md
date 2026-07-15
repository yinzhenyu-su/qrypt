# Developer FAQ

This page records recurring implementation issues and the reasoning behind the
current fixes. It is meant to help contributors find the right layer to inspect
before making changes.

## Finder copy fails when `no_apple_double = true`

### Symptom

Original report:

> If a directory contains a `.DS_Store` file, dragging that directory from
> Finder into the mounted drive may show "无法完成此操作" ("The operation can't be
> completed"). This is a regression.

On macOS, copying a directory with Finder may fail with a generic message such
as "The operation can't be completed" when:

```toml
no_apple_double = true
```

The same copy may work when:

```toml
no_apple_double = false
```

Changing `no_apple_xattr` may not affect this failure.

### Why This Happens

Finder does more than copy regular files. During a directory copy it may create,
probe, update, read, and remove Apple metadata paths such as:

- `.DS_Store`
- `._filename`
- `.Spotlight-V100`
- `.fseventsd`

When `no_apple_double = true`, qrypt should hide or ignore those metadata files
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
  `no_apple_double = true`.
- Finder may remove and recreate the destination directory after an earlier
  failure. That can be a symptom, not the root cause.
- If `no_apple_double = false` works, compare whether the failure is specific to
  the ignored Apple metadata path.

### About `no_apple_xattr`

`no_apple_xattr` controls extended attributes such as:

- `com.apple.FinderInfo`
- `com.apple.quarantine`
- `com.apple.metadata:*`

It is separate from `no_apple_double`.

The mount layer should not tell Finder that `Setxattr` succeeded and then make
the same attribute unreadable in the same mount process unless the option
explicitly ignores it. When `no_apple_xattr = false`, qrypt keeps xattrs in
memory for Finder compatibility. They are not uploaded to the remote drive.

### Regression Tests

Keep tests focused on filesystem semantics rather than one Finder version.
Relevant tests live in `internal/mount/mount_test.go` and should cover:

- Missing AppleDouble lookup returns `ENOENT`.
- Ignored `.DS_Store` supports write, read, offset write, truncate, and rename
  during the same mount process.
- Ignored nested metadata files still create the real parent directory when
  needed.
- Ignored metadata time updates do not touch the backend.
- xattrs can be set, read, listed, removed, renamed, and removed with their
  subtree when `no_apple_xattr = false`.

Run:

```sh
go test ./internal/mount
go test ./...
```

### Practical Guidance

When fixing Finder copy issues, avoid solving them only by checking file names.
Finder depends on a sequence of FUSE semantics. A path should not appear to
exist unless qrypt has a real backend entry or an in-memory node created by an
earlier operation in the same mount process.
