# 支持的驱动

qrypt 支持以下云盘后端。每个驱动通过配置文件中的 `[[mounts]]` 条目使用，`type` 字段指定驱动类型，`params` 中填写对应参数。

## 如何选择驱动

- **先体验 qrypt**：使用 `localfs`，不需要云盘账号，配置示例见[快速上手](quickstart.md)。
- **使用网盘**：从下方列表中选择对应驱动。多数网盘需要从网页端登录后获取 cookie 或 token，具体字段见各驱动小节。
- **使用标准协议或自建服务**：选择 `webdav`、`sftp` 或 `s3`。
- **按能力选驱动**：见[能力对照](#能力对照)。

网盘凭证通常有时效，过期后需要重新获取。请将凭证保存在本机配置文件中，不要提交到版本库。

## 共享凭据

同一账号挂载多个目录时，把账号级参数写进 `[[credentials]]` 一次，再让多个 `[[mounts]]` 通过 `credential` 引用它；路径级参数（如 `root_path`）仍写在各自的 `[mounts.params]` 中。凭据的轮换与刷新由 qrypt 统一维护，不会因为多挂载而各走各的。参数表中的“作用域”列说明每个参数应放在哪一侧：**凭据**、**挂载**或**两者**（两者时挂载值优先）。

```toml
[[credentials]]
name = "main"
type = "quark"
[credentials.params]
cookie = "..."

[[mounts]]
name = "docs"
type = "quark"
credential = "main"
[mounts.params]
root_path = "/Docs"
```

## 驱动列表

| 驱动类型 | 名称 | 必填参数 |
|---|---|---|
| `localfs` | 本地目录 | `root_path` |
| `aliyundrive` | 阿里云盘 | `refresh_token` + `drive_id` |
| `baidu_netdisk` | 百度网盘 | `refresh_token` |
| `onedrive` | OneDrive | `refresh_token` |
| `onedrive_app` | OneDrive 应用权限 | `client_secret` + `tenant_id` + `email` + (`client_id` 或 `client_key`) |
| `quark` | 夸克网盘 | `cookie` |
| `yun139` | 移动云盘 | `authorization` |
| `115` | 115 云盘 | `cookie` |
| `115_open` | 115 云盘（开放平台） | `refresh_token` |
| `123` | 123 云盘 | `username` + `password` |
| `189` | 天翼云盘 189 | `cookie` 或 `username` + `password` |
| `webdav` | WebDAV | `url` + `username` + `password` |
| `sftp` | SFTP | `address` + `username` + (`password` 或 `private_key`) |
| `s3` | Amazon S3 / 兼容 S3 | `bucket` + `endpoint` + `access_key_id` + `secret_access_key` |

## 能力对照

以下矩阵用于选型参考，不影响基本使用。

| 驱动 | 写入 | 断点续传 | 服务端复制 | 服务端哈希 | 保留修改时间 | 空间查询 | 离线下载 |
|---|---|---|---|---|---|---|---|
| `localfs` | ✅ | — | ✅ | — | ✅ | ✅ | — |
| `aliyundrive` | ✅ | ✅ | ✅ | ✅ | — | ✅ | — |
| `baidu_netdisk` | ✅ | ✅ | ✅ | ✅ | — | ✅ | — |
| `onedrive` | ✅ | ✅ | ✅ | ✅ | — | ✅ | — |
| `onedrive_app` | ✅ | ✅ | ✅ | ✅ | — | ✅ | — |
| `quark` | ✅ | ✅ | ✅ | — | — | ✅ | ✅ |
| `yun139` | ✅ | ✅ | — | — | — | ✅ | — |
| `115` | ✅ | ✅ | ✅ | ✅ | — | ✅ | ✅ |
| `115_open` | ✅ | ✅ | ✅ | — | — | ✅ | ✅ |
| `123` | ✅ | ✅ | ✅ | ✅ | — | ✅ | — |
| `189` | ✅ | ✅ | ✅ | — | — | ✅ | — |
| `webdav` | ✅ | — | ✅ | — | — | ✅ | — |
| `sftp` | ✅ | ✅ | — | — | ✅ | ✅ | — |
| `s3` | ✅ | ✅ | ✅ | — | — | — | — |

✅ 支持，— 不支持；不支持的能力会由上层降级或返回 unsupported。

**写入**：创建、删除、移动、重命名；**服务端复制**：由存储端直接复制，不经过本地下载再上传；**服务端哈希**：可直接读取存储端内容哈希用于校验；**保留修改时间**：上传或复制后保留源文件 mtime。

`quark` 的离线下载同时支持解析、选择性转存和加速取回；`115` / `115_open` 支持提交、观察和取回，但不支持解析预览、文件选择和加速。

## localfs

本地目录。

```toml
[mounts.params]
root_path = "/tmp/qrypt-remote"
```

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `root_path` | string | 挂载 | 是 | 本地文件系统根目录路径 |

---

## aliyundrive

阿里云盘。

```toml
[mounts.params]
refresh_token = "your-refresh-token"
drive_id = "your-drive-id"
# root_path = "/qrypt"
```

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `refresh_token` | string (secret) | 凭据 | 是 | 网盘 refresh token，用于 OAuth 认证 |
| `drive_id` | string | 两者 | 是 | 阿里云盘 drive ID |
| `root_path` | string | 挂载 | 否 | 虚拟根路径，启动时解析为网盘文件夹 ID，默认 `/` |
| `order_by` | string | 挂载 | 否 | 列表排序字段 |
| `order_direction` | string | 挂载 | 否 | 排序方向（ASC 或 DESC） |
| `api_base_url` | string | 两者 | 否 | 自定义 API 基础 URL |
| `auth_url` | string | 两者 | 否 | 自定义 OAuth token URL |

---

## baidu_netdisk

百度网盘。

```toml
[mounts.params]
refresh_token = "your-refresh-token"
# root_path = "/qrypt"
```

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `refresh_token` | string (secret) | 凭据 | 是 | 网盘 refresh token，用于 OAuth 认证 |
| `access_token` | string (secret) | 凭据 | 否 | 可选的初始 access token；过期后自动刷新 |
| `root_path` | string | 挂载 | 否 | 作为挂载根的百度网盘路径，默认 `/` |
| `order_by` | string | 挂载 | 否 | 列表排序字段，默认 `name` |
| `order_direction` | string | 挂载 | 否 | 排序方向（asc 或 desc），默认 `asc` |
| `use_online_api` | boolean | 两者 | 否 | 使用 OpenList 兼容的在线 token 刷新接口，默认 `true` |
| `online_api` | string | 两者 | 否 | 在线 token 刷新接口 URL |
| `upload_api` | string | 两者 | 否 | 百度 PCS 上传接口基础 URL |
| `client_id` | string (secret) | 两者 | 否 | OAuth 应用 client ID |
| `client_secret` | string (secret) | 两者 | 否 | OAuth 应用 client secret |
| `api_base_url` | string | 两者 | 否 | 自定义 API 基础 URL |
| `oauth_url` | string | 两者 | 否 | 自定义 OAuth token URL |
| `download_user_agent` | string | 挂载 | 否 | 百度网盘下载请求使用的 User-Agent，默认 `pan.baidu.com` |

---

## onedrive

OneDrive。

```toml
[mounts.params]
refresh_token = "your-refresh-token"
# root_path = "/qrypt"
```

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `refresh_token` | string (secret) | 凭据 | 是 | 网盘 refresh token，用于 OAuth 认证 |
| `access_token` | string (secret) | 凭据 | 否 | 可选的初始 access token；过期后自动刷新 |
| `region` | string | 两者 | 否 | Microsoft 云区域（global、cn、us 或 de），默认 `global` |
| `root_path` | string | 挂载 | 否 | 作为挂载根的 OneDrive 路径，默认 `/` |
| `use_online_api` | boolean | 两者 | 否 | 使用 OpenList 兼容的在线 token 刷新接口，默认 `true` |
| `online_api` | string | 两者 | 否 | 在线 token 刷新接口 URL |
| `client_id` | string (secret) | 两者 | 否 | OAuth 应用 client ID |
| `client_key` | string (secret) | 两者 | 否 | client_id 的别名 |
| `client_secret` | string (secret) | 两者 | 否 | OAuth 应用 client secret |
| `redirect_uri` | string | 两者 | 否 | OAuth 回调地址 |
| `api_base_url` | string | 两者 | 否 | 自定义 API 基础 URL |
| `oauth_base_url` | string | 两者 | 否 | 自定义 Microsoft OAuth 基础 URL |
| `is_sharepoint` | boolean | 挂载 | 否 | 挂载 SharePoint 站点，而不是当前用户的 OneDrive，默认 `false` |
| `site_id` | string | 挂载 | 否 | SharePoint 站点 ID（is_sharepoint=true 时使用） |
| `custom_host` | string | 两者 | 否 | 下载链接使用的自定义域名（如 CDN） |
| `chunk_size` | integer | 挂载 | 否 | 大文件上传分片大小（MiB），默认 `5` |
| `disable_disk_usage` | boolean | 挂载 | 否 | 关闭网盘容量查询，默认 `false` |

---

## onedrive_app

OneDrive 应用权限。

```toml
[mounts.params]
# 认证方式任选其一：`client_id` 或 `client_key`
client_id = "your-client-id"
# client_key = "your-client-id"
client_secret = "your-client-secret"
tenant_id = "your-tenant-id"
email = "user@example.com"
# root_path = "/qrypt"
```

必填条件：`client_secret` + `tenant_id` + `email` + (`client_id` 或 `client_key`)。

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `client_id` | string (secret) | 凭据 | 条件 | OAuth 应用 client ID |
| `client_key` | string (secret) | 凭据 | 条件 | client_id 的别名 |
| `client_secret` | string (secret) | 凭据 | 是 | OAuth 应用 client secret |
| `tenant_id` | string | 凭据 | 是 | Microsoft Entra 租户 ID |
| `email` | string | 凭据 | 是 | 要挂载的账号邮箱 |
| `region` | string | 两者 | 否 | Microsoft 云区域（global、cn、us 或 de），默认 `global` |
| `root_path` | string | 挂载 | 否 | 作为挂载根的 OneDrive 路径，默认 `/` |
| `api_base_url` | string | 两者 | 否 | 自定义 API 基础 URL |
| `oauth_base_url` | string | 两者 | 否 | 自定义 Microsoft OAuth 基础 URL |
| `custom_host` | string | 两者 | 否 | 下载链接使用的自定义域名（如 CDN） |
| `chunk_size` | integer | 挂载 | 否 | 大文件上传分片大小（MiB），默认 `5` |
| `disable_disk_usage` | boolean | 挂载 | 否 | 关闭网盘容量查询，默认 `false` |

---

## quark

夸克网盘。

```toml
[mounts.params]
cookie = "k1=v1; k2=v2"
# root_path = "/qrypt"
```

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `cookie` | string (secret) | 凭据 | 是 | 网盘认证 cookie，从网页端登录后获取 |
| `root_path` | string | 挂载 | 否 | 网盘上的虚拟根路径，默认 `/` |
| `base_url` | string | 两者 | 否 | 自定义 API 基础 URL |
| `v2_url` | string | 两者 | 否 | 自定义 API v2 URL |
| `wg_keys` | string | 挂载 | 否 | 离线请求使用的 wg data keys，格式为 secureNo:hexkey，逗号分隔 |

---

## yun139

移动云盘。

```toml
[mounts.params]
authorization = "your-authorization-token"
# root_path = "/qrypt"
```

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `authorization` | string (secret) | 凭据 | 是 | 移动云盘 authorization token，从网页端登录后获取 |
| `root_path` | string | 挂载 | 否 | 虚拟根路径，启动时解析为网盘文件夹 ID，默认 `/` |
| `root_id` | string | 挂载 | 否 | 预解析的文件夹 ID；设置后忽略 root_path |

---

## 115

115 云盘。

```toml
[mounts.params]
cookie = "k1=v1; k2=v2"
# root_path = "/qrypt"
```

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `cookie` | string (secret) | 凭据 | 是 | 网盘认证 cookie，从网页端登录后获取 |
| `root_path` | string | 挂载 | 否 | 虚拟根路径，启动时解析为网盘文件夹 ID，默认 `/` |

---

## 115_open

115 云盘（开放平台）。

115 云盘（开放平台 API，`refresh_token` 认证）。使用官方 [115 开放平台](https://open.115.com) 的 Bearer token 认证，token 过期自动刷新并持久化，无需手动更新 cookie。

**获取 refresh_token**:在 [open.115.com](https://open.115.com) 注册应用，或使用公共应用 ID，通过 PKCE 设备码扫码（115 App 扫码授权）获得 `access_token` 与 `refresh_token`。注意：同一应用同一账号最多持有 2 个 refresh_token，第 3 次获取会使第 1 个失效；每次刷新都会轮换（旧 refresh_token 立即作废）。
```toml
[mounts.params]
refresh_token = "eg2t4.<hex>.<hex>"
# root_path = "/qrypt"
```

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `refresh_token` | string (secret) | 凭据 | 是 | 网盘 refresh token，用于 OAuth 认证 |
| `access_token` | string (secret) | 凭据 | 否 | 可选的初始 access token；过期后自动刷新 |
| `root_path` | string | 挂载 | 否 | 虚拟根路径，启动时解析为网盘文件夹 ID，默认 `/` |
| `limit_rate` | integer | 挂载 | 否 | 限制 API 请求速率（每秒 N 次），默认 `2` |

---

## 123

123 云盘。

```toml
[mounts.params]
username = "13800138000"
password = "your-password"
# root_path = "/qrypt"
```

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `username` | string | 凭据 | 是 | 123 云盘账号（手机号或邮箱） |
| `password` | string (secret) | 凭据 | 是 | 123 云盘密码 |
| `root_path` | string | 挂载 | 否 | 网盘上的虚拟根路径（设置 root_id 时忽略），默认 `/` |
| `root_id` | string | 挂载 | 否 | 预解析的文件夹 ID；设置后忽略 root_path |
| `base_url` | string | 两者 | 否 | 自定义 API 基础 URL |

---

## 189

天翼云盘 189。

```toml
[mounts.params]
# 认证方式任选其一：`cookie` 或 `username` + `password`
cookie = "k1=v1; k2=v2"
# username = "18912345678"
# password = "your-password"
# root_path = "/qrypt"
```

必填条件：`cookie` 或 `username` + `password`。

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `cookie` | string (secret) | 凭据 | 条件 | 网盘认证 cookie，从网页端登录后获取 |
| `username` | string | 凭据 | 条件 | 189 云盘账号（手机号） |
| `password` | string (secret) | 凭据 | 条件 | 189 云盘密码 |
| `root_path` | string | 挂载 | 否 | 网盘上的虚拟根路径（设置 root_id 时忽略），默认 `/` |
| `root_id` | string | 挂载 | 否 | 预解析的文件夹 ID；设置后忽略 root_path |

---

## webdav

WebDAV。

```toml
[mounts.params]
url = "https://nextcloud.example.com/remote.php/dav/files/user"
username = "user"
password = "your-password-or-app-token"
# root_path = "/qrypt"
```

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `url` | string | 两者 | 是 | WebDAV 服务器基础 URL |
| `username` | string | 凭据 | 是 | WebDAV 用户名 |
| `password` | string (secret) | 凭据 | 是 | WebDAV 密码或应用 token |
| `root_path` | string | 挂载 | 否 | WebDAV 基础 URL 下的可选子路径，默认 `/` |

---

## sftp

SFTP。

```toml
[mounts.params]
# 认证方式任选其一：`password` 或 `private_key`
address = "sftp.example.com:22"
username = "user"
password = "your-password"
# private_key = "~/.ssh/id_ed25519"
# root_path = "/data/qrypt"
```

必填条件：`address` + `username` + (`password` 或 `private_key`)。

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `address` | string | 两者 | 是 | SFTP 服务器地址，包含端口 |
| `username` | string | 凭据 | 是 | SFTP 用户名 |
| `password` | string (secret) | 凭据 | 条件 | SFTP 密码；与 private_key 二选一 |
| `private_key` | string (secret) | 凭据 | 条件 | SSH 私钥路径或 PEM 内容；与 password 二选一 |
| `passphrase` | string (secret) | 凭据 | 否 | SSH 私钥口令 |
| `known_hosts` | string | 两者 | 否 | 可选的 OpenSSH known_hosts 文件；省略时使用 ~/.ssh/known_hosts |
| `root_path` | string | 挂载 | 否 | 作为挂载根的远程目录，默认 `/` |

---

## s3

Amazon S3 / 兼容 S3。

```toml
[mounts.params]
bucket = "my-bucket"
endpoint = "https://s3.us-east-1.amazonaws.com"
access_key_id = "AKIA..."
secret_access_key = "..."
# root_path = "/my-mount"
```

| 参数 | 类型 | 作用域 | 必填 | 说明 |
|---|---|---|---|---|
| `bucket` | string | 挂载 | 是 | S3 bucket 名称 |
| `endpoint` | string | 两者 | 是 | S3 服务端点 URL |
| `region` | string | 两者 | 否 | AWS 区域，默认 `us-east-1` |
| `access_key_id` | string (secret) | 凭据 | 是 | S3 访问密钥 ID |
| `secret_access_key` | string (secret) | 凭据 | 是 | S3 访问密钥 Secret |
| `session_token` | string (secret) | 凭据 | 否 | S3 临时凭证的 session token |
| `custom_host` | string | 两者 | 否 | 下载链接使用的自定义域名（如 CDN） |
| `force_path_style` | boolean | 挂载 | 否 | 强制使用 path-style 寻址（MinIO 和多数非 AWS S3 需要），默认 `false` |
| `list_object_version` | string | 挂载 | 否 | S3 列表 API 版本：v1 或 v2，默认 `v1` |
| `placeholder` | string | 挂载 | 否 | 空目录使用的占位文件名，默认 `.qrypt` |
| `root_path` | string | 挂载 | 否 | Bucket 内的根路径前缀，默认 `/` |
| `sign_url_expire` | string | 挂载 | 否 | 预签名下载链接有效期，默认 `4h` |
