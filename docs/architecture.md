# qrypt Architecture

## Boundary Rules

`pkg/vfs` is the kernel. It owns file behavior: read cache, write staging,
pending journal recovery, and upload scheduling. It must not import FUSE,
desktop lifecycle code, or concrete provider SDKs.

`pkg/drive` is the provider contract. Baidu, Aliyun, 115, Quark, 139, and other
Alist-inspired drivers should live under `internal/driver/<name>` and adapt
their API to the same small interfaces.

`pkg/crypt` is a transparent rclone crypt wrapper around any `drive.Driver`.
It handles filename encryption, content encryption, and decryption.

`internal/mount` is the FUSE adapter boundary. A future FUSE implementation
should translate filesystem callbacks into `pkg/vfs` calls without adding
business logic.

## Namespace Mounts

`pkg/vfs.Namespace` lets one OS mount point contain multiple backend instances.
The namespace root lists configured mount names, and all operations dispatch by
the first path segment:

```text
/quark/path/in/quark
/quark2/path/in/quark2
/localfs/path/in/localfs
```

Each mount owns its own `VFS`, driver, encryption settings, staging directory,
pending journal, and read cache. This allows multiple same-type drivers, such
as two Quark accounts, and mixed driver types under the same FUSE mount.

## Recovery Model

Writes are persisted to `<cache>/staging/*.staging` and recorded in
`<cache>/pending.jsonl` before upload. On startup, `VFS.Start` calls `Resume`,
which scans pending entries and requeues unfinished uploads. Completed uploads
remove the staging file and compact the journal.

## Read Cache

Reads are cached in fixed-size chunk batches under `<cache>/reading`. The cache
is LRU-evicted by access time when `CacheMaxBytes` is exceeded.
