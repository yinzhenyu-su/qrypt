# 夸克网盘扫码登录

`qrypt credential login` 用手机扫码获取夸克网盘 cookie，保存为共享凭据，并可顺便创建第一个挂载点。之后同一账号的多个挂载点共用这一份凭据和登录状态。

## 登录并创建挂载

```bash
qrypt credential login --type quark
```

按提示用夸克 App 扫描终端里的二维码。登录成功后：

- 在交互终端里，命令会依次询问挂载名、远端根路径和挂载点，默认值可以直接回车接受。
- 配置会保存到当前找到的配置文件；没有配置文件时创建 `~/.qrypt/qrypt.toml`。
- 完成后运行：

```bash
qrypt mount
```

夸克网盘会出现在 `<挂载点>/<挂载名>`，例如 `~/Qrypt/quark`。

## 非交互使用

标准输入不是终端，或加上 `--no-input` 时，命令不会提问：

```bash
# 只保存凭据，之后再建挂载
qrypt credential login --type quark --no-input

# 同时创建挂载
qrypt credential login --type quark --no-input \
  --mount-name quark --root-path / --mount-point ~/Qrypt
```

已有配置文件时，不提供任何挂载参数就只保存凭据；提供任意一个挂载参数（`--mount-name`、`--root-path`、`--mount-point`）就会用参数加默认值创建挂载。没有任何配置文件时，非交互模式必须提供至少一个挂载参数，否则命令会在扫码前退出。

## 指定凭据名称与账号复用

```bash
qrypt credential login --type quark --name quark-main
```

省略 `--name` 时，如果已有同类型、同账号的凭据，会更新它的 cookie，不会再建一条。`--name` 指向其他账号的凭据时命令会拒绝覆盖；如果已有凭据的账号无法解析，则按显式覆盖更新，并打印警告。

## 多个挂载点共用一个凭据

登录生成的凭据可以在配置里被多个挂载点引用：

```toml
[[credentials]]
name = "quark"
type = "quark"

[credentials.params]
cookie = "..."

[[mounts]]
name = "quark-docs"
type = "quark"
credential = "quark"
[mounts.params]
root_path = "/Docs"

[[mounts]]
name = "quark-photos"
type = "quark"
credential = "quark"
[mounts.params]
root_path = "/Photos"
```

也可以直接新增挂载：

```bash
qrypt mount add quark-photos --credential quark --param root_path=/Photos
```

## 常见问题

- **二维码过期**：命令会自动换一张二维码，最多刷新 3 次。
- **登录失败或超时**：配置不会被修改，重新运行同一条命令即可。
- **cookie 校验失败**：登录结果在写入前会真实验证；失败时不会保存不可用的 cookie。仍可手动执行 `qrypt credential add --param cookie=...` 作为兜底。
- **想撤销登录**：用 `qrypt credential remove <名称>`；它要求没有挂载点引用该凭据。
