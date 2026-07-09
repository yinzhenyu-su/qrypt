# 命令行参考

## 通用约定

- 配置文件参数只出现在需要配置的具体命令上，不是根命令全局参数。
- 省略 `--config` 时，依次查找：
  1. `./qrypt.toml`
  2. `~/.qrypt/qrypt.toml`
  3. Unix：`$XDG_CONFIG_HOME/qrypt/qrypt.toml`，默认 `~/.config/qrypt/qrypt.toml`
  4. Windows：`%AppData%\qrypt\qrypt.toml`
- `REMOTE` 表示 qrypt 虚拟文件系统路径；`LOCAL` 表示本机路径。
- `fs get` 的 `LOCAL` 为 `-` 时写入标准输出。
- `fs put` 的 `LOCAL` 为 `-` 时读取标准输入。
- 支持 `--json` 的查询命令会向标准输出写入稳定的 JSON，状态提示和错误写入标准错误。
- 路径参数中的 `~` 会被展开为用户主目录。

## 配置

创建配置文件：

```sh
qrypt config init [PATH]
```

默认写入 `./qrypt.toml`。已有文件不会被覆盖；确认覆盖时使用 `--force`。

验证配置但不连接远端：

```sh
qrypt config validate [--config PATH]
```

验证包括未知配置键、配置版本、mount 名称、驱动类型、驱动必填参数、加密、缓存和带宽设置。

查看已屏蔽密码等敏感字段的配置：

```sh
qrypt config show [--config PATH]
```

导出 rclone 兼容密码：

```sh
qrypt config export-rclone-password MOUNT_NAME [--config PATH]
qrypt config export-rclone-password --password-file PATH [--salt SALT]
printf '%s' "$PASSWORD" | qrypt config export-rclone-password --password-stdin
```

直接输入密码时优先使用 `--password-file` 或 `--password-stdin`。`--password` 会暴露在 shell 历史和进程参数中。

## 挂载

```sh
qrypt mount [MOUNTPOINT] [--config PATH] [--socket PATH]
```

省略 `MOUNTPOINT` 时读取配置中的 `mount_point`。`--socket` 启动本机调试接口。

## 文件系统操作

```sh
qrypt fs list [REMOTE] [--json]
qrypt fs stat REMOTE [--json]
qrypt fs cat REMOTE
qrypt fs get REMOTE LOCAL [--force]
qrypt fs put LOCAL REMOTE [--wait-timeout DURATION]
qrypt fs mkdir REMOTE
qrypt fs mv SOURCE DESTINATION
qrypt fs rm REMOTE [--wait-timeout DURATION]
qrypt fs pending [--verbose | --json]
```

`fs` 命令组支持 `--config PATH`，既可写在子命令前，也可写在子命令后：

```sh
qrypt fs --config PATH list /
qrypt fs list / --config PATH
```

`get` 默认拒绝覆盖本机文件；明确覆盖时使用 `--force`。`put` 和 `rm` 会等待异步远端操作完成，可通过 `--wait-timeout` 调整最长等待时间。

## 驱动信息

```sh
qrypt driver list [--json]
qrypt driver schema NAME [--json]
```

`schema` 展示驱动参数类型、必填项、默认值、示例和敏感字段标记。

## 调试

运行时调试命令必须提供 `--socket PATH`：

```sh
qrypt debug collect [REMOTE] --socket PATH
qrypt debug inspect REMOTE --socket PATH
qrypt debug watch [REMOTE] --socket PATH
qrypt debug raw ENDPOINT --socket PATH
qrypt debug bundle [REMOTE] --socket PATH --out FILE [--force]
```

离线 journal 检查使用配置文件或明确的缓存目录：

```sh
qrypt debug journal [--config PATH | --cache-dir PATH] [--json]
```

详细说明见[调试文档](../for-developer/debug.md)。

## 版本与补全

```sh
qrypt version [--json]
qrypt --version
qrypt completion bash
qrypt completion zsh
qrypt completion fish
qrypt completion powershell
```
