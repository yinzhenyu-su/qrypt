---
name: qrypt-temp-hygiene
description: Reclaim qrypt test and isolated-mount temp residue safely — sweep leftover homes, session logs, sockets and dead FUSE mounts without deleting through a live mount. Use when TMPDIR/tmp keeps growing, CI reports disk pressure, a killed test leaves qrypt-* dirs behind, or an isolated mount is stuck after SIGKILL.
---

# Qrypt 临时目录与残留挂载清理

测试和 `--isolated` 挂载会创建临时 home、缓存、日志、socket 和 FUSE 挂载点。
正常退出由 `t.Cleanup`/defer 回收；**被 SIGKILL 或 `-timeout` 杀掉时不会**，
这是残留的主要来源。清理的第一原则：**先确认不是活挂载，再删**——穿过活挂载的
`rm -rf` 会删到云端数据。

## 1. 快速自愈

```bash
scripts/ci/test-layers.sh clean-temp     # 每层 CI 开始时也会自动跑一次

# 只清 10 分钟以前的残留（默认）；确认没有其它测试在跑时才用 0
QRYPT_TEMP_MAX_AGE_MIN=0 scripts/ci/test-layers.sh clean-temp

# 死挂载（owner 确认进程已退出）才强制卸载
QRYPT_TEMP_MAX_AGE_MIN=0 QRYPT_TEMP_FORCE_UNMOUNT=1 scripts/ci/test-layers.sh clean-temp
```

清扫范围（`scripts/ci/test-layers.sh` 的 `clean_orphan_temp`）：`qrypt-cli-test-home-*`、
`qrypt-core-test-logs`、`qrypt-mobile-test-logs`、`qrypt-receive-test-*`、
`qrypt-share-test-*`、`qrypt-offline-test-*`、`qrypt-cache-*`、`qrypt-isolated-*`、
`qrypt-generated-*`、`qrypt-install.*`、`qrypt-smoke.*`、`qrypt-shellcheck.*`、
`qrypt-read-ab-*`、`qrypt-tune-*`、`qrypt-mount-test.*`，以及 socket
`qrypt-[0-9]*.sock`/`qrypt-test-[0-9]*.sock`。同时扫 `$TMPDIR` 和 `/tmp`
（两者可能不同，debug-mount 在 socket 路径过长时会退回 `/tmp`）。

## 2. 安全规则（踩过的坑）

- **活挂载永不自动卸载**：扫描到仍活动的挂载时打印
  `clean-orphan-temp: leaving still-mounted temp dir: <dir>` 并保留；只有显式
  `QRYPT_TEMP_FORCE_UNMOUNT=1` 才尝试 detach，失败也不删。
- **canonical path 比对**：macOS 上 `mount` 表写 `/private/var/...`，测试创建的是
  `/var/...`，直接用字面路径会漏判。`mount_point_active` 必须同时比较字面路径与
  父目录解析后的 canonical 路径；detach 也要重试 canonical 路径。
- **年龄门槛**：默认只清 10 分钟以前的条目，避免误删并行运行的测试；BSD find 没有
  “任意年龄”谓词，`QRYPT_TEMP_MAX_AGE_MIN<=0` 时用 `-mmin -5256000` 代替。
- **不靠 `rm -rf` 通配**：先按前缀枚举、再 `find -maxdepth 0` 判龄、最后删除；
  挂载目录单独走 `reclaim_mounted_temp`。
- 只清 qrypt 自己的命名空间；`$HOME`、用户真实挂载 `~/Qrypt`、`~/.qrypt` 不在清扫范围。

## 3. 隔离挂载的生命周期

`qrypt mount --isolated`（`internal/cli/mount/mount.go`）：

- 创建 `qrypt-isolated-*` 临时 home，设置 `QRYPT_HOME`，只读挂到 `<root>/mnt`；
- 优雅退出（SIGINT/SIGTERM/SIGHUP，见 `internal/cli/signals_unix.go`）会卸载并删除 home；
- `kill -9` 会留下 home + 一条守护进程已死的 macFUSE 挂载（`ls` 报
  `Device not configured`）；
- 正常路径会先 detach 残留挂载再删 home（`removeIsolatedHome`，失败则保留并提示），
  不让用户面对活挂载下的 `RemoveAll`；
- 想保留现场用 `--isolated-keep`。

手工恢复死挂载：先确认没有 qrypt 进程，再
`umount -f <mountpoint>`（Darwin 可 `diskutil unmount force`），最后删 home；
`pkg/mount.DetachMount`/`FuseMountRoot` 是同一逻辑的代码入口。

## 4. 脚本自己的临时文件

- `scripts/ci/test-layers.sh` 的所有 `mktemp` 走 `new_temp_dir`/`new_temp_file` 登记，
  由 `EXIT/INT/TERM` trap 统一回收；改脚本时不要新增裸 `mktemp`。
- `scripts/dev/debug-mount.sh` 的 `cleanup()` 在挂载仍活动时先 `detach_mount`，
  失败才保留 `WORKDIR` 并以非零退出。
- `scripts/ci/install-tools.sh`、`scripts/release/gen-changelog.sh` 的临时文件均带
  `trap rm -rf`；`check-all.sh` 的 schema/docs 步骤在各自 `bash -c` 子 shell 内配对 trap。
- 编辑正在被 bash 执行的脚本会导致 “bad file descriptor”/退出 127：等当前层跑完再改。

## 5. 验证清理逻辑（不碰真实数据）

1. 合成夹具：在 `$TMPDIR` 建旧目录（`touch -t 202601010000`）造各前缀残留 +
   一个带 `mount/` 子目录的 `qrypt-mount-test.*`，跑 `clean-temp`，确认旧目录被删、
   新目录保留。
2. 活挂载保护：对带死挂载的目录，默认 `clean-temp` 应打印保留信息且目录仍在；
   `QRYPT_TEMP_FORCE_UNMOUNT=1` 时才卸载并删除。
3. 中断路径：用 `start_new_session=True` + `killpg(SIGINT)` 跑一层，确认退出码 130、
   登记过的临时路径全部回收、`mount` 表无残留。
4. 复盘：`du -sh $TMPDIR/qrypt-* /tmp/qrypt-* 2>/dev/null` 应为空或只剩用户自己的文件。

文档入口：`docs/for-developer/quality-gates.md` 的“临时目录与残留挂载”一节。
