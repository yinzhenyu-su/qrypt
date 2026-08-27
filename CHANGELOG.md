# Changelog

## v0.5.0 (2026-08-27)

### Features
- feat(vfs): mount contract, consumer capabilities, and dynamic mounts
- feat(mount): 用平台挂载选项替代苹果元数据模拟
- feat(sftp): 支持断线自动重连与修复分段上传结果不清空的问题
- feat(drivers): 新增 SFTP 驱动支持密码和私钥认证

### Bug fixes
- fix(windows): sftp mock rename IDs and xfer benchmark read_bps
- fix(build): statically link linux binaries so release tarballs run on any distro
- fix(build): netgo DNS resolver in static glibc builds to avoid getaddrinfo SIGFPE
- fix(test): windows-robust fixture reads and reader cleanup for coverage tests
- fix(windows): last three windows CI failures
- fix(windows): close remaining windows gaps in core logs, control client, sftp, journal
- fix(windows): port remaining packages to Windows paths and clock
- fix(vfs): keep virtual paths slash-based on Windows
- fix(test): escape Windows paths in test TOML configs via shared util.TOMLPath
- fix(mobile): make test configs Windows-safe (TOML escaping, log-dir lock)
- fix(sftp): 超时仅关闭无响应连接，上传加超时保护
- fix(sftp): 修复读操作超时与EOF处理，删除失败回滚并重试
- fix(sftp): 列目录断连后自动重连并重试

### Others
- test(vfs): coverage for namespace/runtime surfaces, restore release coverage floor
- test(crypt): rclone compat golden fixtures and interop tests, empty-password key path
- cli: typed runtime seam, role interfaces, shared flag helper
- refactor(vfs): narrow adapter surface and drop redundant wrappers
- chore(vfs): drop unused uploadSnapshotState alias after debug runtime slim-down
- refactor(vfs): narrow staging and debug adapters
- chore(ci): fix staticcheck and gofmt findings
- refactor(vfs): remove dead aliases left by the view-domain split
- refactor(vfs): split layer domains; rework upload session; add windows ci
- test(sftp): 断言清理阶段错误并修复断点续传恢复标记
- test: 使用 b.Loop 替代固定迭代计数器
- test: 添加模糊测试用例并更新夜间模糊测试脚本
- ci: 缓存 CI 工具并矩阵化测试任务
- test(media): 使用 b.Loop 与 limits 常量优化 MP4 测试基准
- docs(drivers): 支持 189 网盘账号密码登录与 alternate 必填参数文档生成

---

## v0.4.0 (2026-08-24)

### Features
- feat(ci): 添加本地 CI 检查脚本以复现 CI 检查流程
- feat(core): 支持可配置的读取分块大小并用于大文件分块读取
- feat(vfs): scope sequential read tracking per open-file session
- feat(fuse): invalidate kernel path caches after async uploads
- feat(read): add seek test pattern with concurrent scenario support
- feat(vfs/read): add sequential read detection and adaptive prefetch
- feat(vfs): add mounted read test with cache mode support
- feat(vfs): add batch upload/move tests and target index cache
- feat(control): report build version on /v1/health
- feat(logging): render log timestamps with an explicit UTC offset
- feat(control): expose /v1/tasks debug endpoint with recovery linkage
- feat(core): recover staging uploads and retry direct uploads in place
- feat(upload): direct-upload integrity checks, read-cache seeding, raw reads
- feat(drivers): scopedfs — platform-authorized directory as a qrypt drive
- feat(upload): direct source-upload stream tasks with restart recovery
- feat(drivers): server-side Copy for onedrive, aliyundrive, baidunetdisk,   p115, p115open, p189
- feat(drivers): server-side Copy for webdav, s3, quark
- feat(drivecopy): route same-driver copies through provider-side Copy
- feat(vfs): add streaming ReadStream for bounded-memory downloads
- feat(config): default log_file/error_file to <storage.log_dir>/qrypt*.log
- feat(cache): disable read cache with max_size=0, default 2G
- feat(drive): stable error sentinels across driver and task boundaries
- feat(drivers): add internal/util/httpclient and unify request plumbing
- feat(drive): fault injection toolkit on FakeDriver
- feat(drive,core): unify error taxonomy with permission/persistence categories
- feat(drive,control,cli): driver behavioral contract suite
- feat(cli): add --compare strategy to fs check and fs sync
- feat(cli): fs sync --resume continues interrupted sessions
- feat(drivers,cli): content-hash verification for sync/check
- feat(cli): fs sync one-way tree synchronization
- feat(control): unify fs/resume specs into the test-spec registry
- feat(control): unified test-run envelope and capability-matrix scheduler
- feat(control): shared test fixture and multipart driver test
- feat(cli): add fs check with non-download hash verification
- feat(cli): add qrypt config path; drop per-command config path prints
- feat(cli): add fs df/du/find, --bwlimit flags and human-readable sizes
- feat(cli): add fs crypt-encode/crypt-decode for backend filename comparison
- feat(cli): add layered exit codes, copy dry-run, journal replay/prune, jsonl streaming

### Bug fixes
- fix(vfs): defer upload scheduling until Start
- fix(vfs): sweep unreferenced staging files while running
- fix(mount): enable auto_xattr on Darwin for macOS 26.6.1 Finder copies
- fix(core): apply De Morgan's law to satisfy QF1001 in random access check
- fix(vfs): path-lock lifecycle, overlay index, concurrent close
- fix(vfs): unify listing order — dirs first, case-insensitive names
- fix(ci): remove dead compat shims, update golangci stutter exclusions
- fix(vfs): complete teardown in TestDomainCloseStopsScheduledTimers
- fix(vfs): de-flake TestDomainCloseStopsScheduledTimers arming check
- fix(cli): attempt FUSE unmount on every exit path, not just signals
- fix(cli): test NTP gate lost to EffectiveNTPEnabled override
- fix(cli): keep tests off NTP DNS to stop goleak flakes
- fix(ci): regenerate config docs from schema, harden test cache teardown
- fix(mobile): tie API contexts to the session lifecycle
- fix(drivers): unify redaction on util.Snippet, audit remaining leak sites
- fix(mount): skip volname option on Linux
- fix(config): reject NaN in ParseSize
- fix(config): stop shadowing the builtin max in fuzz test
- fix(drivers): mask raw response bodies in error strings
- fix(crypt): segment long encrypted filenames instead of panicking
- fix(timeutil): make NTP sync loop interruptible and drained on stop
- fix(vfs): make Start idempotent with lifecycle tests
- fix(drivers): classify not-found and respect ctx in 5 drivers
- fix(vfs): blocking upload-queue enqueue leaks on shutdown
- fix(quark): strip OSS signature from upload-part debug log
- fix(core,vfs,control): stop background workers on shutdown
- fix(drivers): classify not-found errors with vfs.ErrNotFound
- fix(cli): sync review findings — hash error handling, vfs mtime, delete race, ok/exit parity, journal writes
- fix(cli): fs sync waits for uploads without a deadline
- fix(task): never cancel a succeeded upload when dismissing its task
- fix(vfs): journal prune no longer resurrects finished uploads
- fix(mobile): partial upload config merge, dangling upload tasks, session core race
- fix(cli): df json keeps failing mounts, du on single files, find canonical paths

### Others
- refactor(drivers): 统一不可重试客户端状态判断到 httputil 辅助函数
- fix: 分块读取大型 MP4 元数据以免超出单次读取限制
- ci: 升级 golangci-lint 到 v2.13.1 并精简版本固定文档
- test: 在 testRuntimeLayout 中设置 RootDir 为 storage 子目录
- perf: 将读取缓存块大小调整为 512 KiB 并优化大文件下载
- test: 改用 b.Loop 编写 fetch 基准测试
- revert: 撤销有问题的 mobile 接口变更
- perf: tune hot chunk retention
- test: adapt read windows to tuned chunk settings
- perf: tune adaptive remote video reads
- feat: improve mobile read pipeline
- build: upgrade project to Go 1.27
- perf(read): cut seek latency via CDN warmup and merged chunk reads
- style(vfs): apply gofmt to visibility_overlay.go
- docs(scripts): generate full-config [mount] section from mountOptionsConfig
- refactor(config): group mount options under [mount] table
- refactor(media): resolve chunk offset rewrite via metadata-only fixed point
- chore(third_party): apply modern gofmt to cgofuse
- docs(scripts): describe work_dir derivation and QRYPT_HOME in config docs
- refactor(vfs): retire superseded staging generations at schedule time
- refactor(contracttest): improve read test cleanup and add retry helper
- refactor(contracttest): improve read test cleanup and data output
- docs(debug): add mount-debug-test.sh usage guide
- style(vfs): gofmt alignment and modernize test loops
- ci: pin Go 1.26.6 toolchain and fix dist ldflags package path
- perf(vfs): allocation-free virtual path parent traversal
- perf(core): parallel mount init, async statfs, fast mount ready
- Handle busy debug server without failing core open
- refactor(api): lift internal packages to pkg, split CLI subcommands
- ci: migrate to golangci-lint v2 and fix lint failures
- refactor(vfs): layered VFS with domain packages + transaction model
- test(speed): cut ~13s from yun139, vfs, and core test suites
- perf(vfs): shard view entries, sync.Map path locks, O(1) pending lookup
- test(drivers): include server_side_copy in localfs capability contract
- refactor(internal): rebalance internal package layering
- test(vfs): verify disabled read cache keeps the full file lifecycle
- docs(faq): add TL;DR to write-amplification section
- docs(faq): explain cross-drive copy write amplification
- test(vfs): assert blocked-enqueue shutdown semantics, fix listing docs
- refactor(vfs): split listing domain, rename debug, harden domain close
- refactor(vfs): move construction/teardown into domain states, document ownership
- refactor(vfs): split namespace routes and group domain state
- refactor: drop Upload prefix from uploadsession exports
- refactor(drivers): promote shared driver util to internal/util
- ci: enforce architecture boundaries with scripts/check-arch.sh
- refactor(drivers): centralize HTTP error redaction in util.HTTPError
- ci: pin govulncheck and all GitHub Actions for reproducibility
- ci: single source of truth for test package lists
- ci: fail the build when generated user docs are stale
- test(drive): local contract matrix for every driver + fake ResolvePath fix
- refactor(control): split driver_benchmark.go into five files
- ci: add PR-stage coverage snapshot to CI summary
- ci(contract): write mounts output via GITHUB_OUTPUT
- ci(contract): use repository variable for mount matrix, not a secret
- refactor(control): split server_handlers.go by debug domain
- ci: nightly quality gates and contract-test workflow
- test(core): cross-layer error taxonomy contract
- test(vfs): three-generation visibility and delete-retry-vs-restore
- ci(fuzz): nightly fuzz runner with corpus preservation
- ci(coverage): enforce per-package coverage floors
- test(sync): session lifecycle round-trip and TTL pruning
- docs(vfs): pin Namespace.Start propagation semantics
- ci(scripts): per-package timing report and coverage baseline script
- test(fuzz): property fuzzers for paths, parsers and error taxonomy
- test(sync): dry-run produces no remote mutation
- test(vfs): state-machine coverage for the five reliability gaps
- ci(scripts): warn when a test layer exceeds 20s
- refactor(control): narrow smoke/resume helpers to any + capability asserts
- test(vfs): real resume-once coverage and context ownership test
- ci(scripts): add integration layer and per-layer timing to test-layers
- refactor(core): export BuiltFileSystem, narrow walkTaskTree to Reader
- refactor(vfs): split lifecycle and cache control out of FileSystem
- refactor(sync): extract fs sync into a standalone domain package
- ci: add layered test entry script
- test: add in-memory FakeDriver, adopt it in VFS/core tests
- test(cli): isolate tests from the real home directory
- refactor(vfs): name optional capability interfaces, drop anonymous assertions
- test(vfs): drain upload worker with waitNoPending in SetModTime test
- ci: add golangci-lint and govulncheck gates
- refactor(drivers): split remaining oversized driver files
- refactor: split oversized files by responsibility
- refactor(drive,vfs): fix driver→vfs dependency inversion on ErrNotFound
- style: staticcheck cleanup — dead code, simplifications, deprecations
- test: add goleak goroutine-leak verification to core packages
- test(cli): cover config show secret redaction
- test(vfs): fix TestSetModTimeAppliesToUpload staging cleanup race
- ci: enforce gofmt and extend race coverage to CLI/core/mobile/crypt/task
- style: gofmt the seven unformatted files

---

## v0.3.0 (2026-08-03)

### 新驱动
- **115_open**:新增 115 云盘开放平台驱动,使用官方 Bearer token 认证(`refresh_token` + `access_token`),过期自动刷新并持久化,支持秒传、分片上传/断点续传、全局限速。相比 cookie 版 `115` 无需维护过期 cookie。token 获取方式见 [支持驱动](docs/for-user/support-drivers.md)。

### 稳定性
- 凭据状态回退:所有持久化凭据的驱动(115/115_open/quark/189/aliyundrive/baidunetdisk/yun139)在状态文件(如 `115_cookie.json`)过期失效时,自动回退到配置文件中的凭据,只有两者都失效才报错;回退成功后自动更新状态文件。
- 移动端/嵌入式 API 的 deadline 语义统一(`timeout` → `deadline`)。

### 性能
- VFS 运行时重构:单体内核拆分为运行时组件,提取独立 UploadService。
- 削减每操作 CPU:活动操作 map+mutex 换成无锁环形槽、O(1) 待处理查找、分片读缓存锁、日志级别前置检查等(均有 benchmark 支撑)。
- 任务系统:只持久化重要状态变更、降低云进度轮询频率。

### 新功能
- 跨挂载移动任务(move task,copy+delete)。
- 缩略图缓存(优先级感知读槽)。
- MP4 probe 与 fast-start 重写包,虚拟文件 API。
- 移动端桥接:任务列表/取消/重试、StorageUsage、ClearReadCache、Mounts、ReadAtInto、WriteFile、StatJSON 等。
- CLI `fs list --remote-names`,Entry 增加 CreatedAt/UpdatedAt。

### 调试与观测
- TCP debug 端点与 debug 配置段,debug reset。
- 读内存调试 API(热块、写队列),进程 RSS 上报。
- debug 事件中敏感 URL 脱敏。

### 文档
- 支持驱动文档生成器修复:onedrive/onedrive_app/115_open 重新纳入生成,补齐参数示例。
