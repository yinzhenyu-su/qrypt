# 支持的驱动

以下文档由 `qrypt.schema.json` 自动生成。

qrypt 支持以下云盘后端。每个驱动通过配置文件中的 `[[mounts]]` 条目使用，`type` 字段指定驱动类型，`params` 中填写对应参数。

## 驱动列表

| 驱动类型 | 名称 | 必填参数 |
|---|---|---|
| `localfs` | 本地目录 | `root_path` |
| `aliyundrive` | 阿里云盘 | `refresh_token`, `drive_id` |
| `baidu_netdisk` | 百度网盘 | `refresh_token` |
| `onedrive` | OneDrive | `refresh_token` |
| `onedrive_app` | OneDrive 应用权限 | `client_id` 或 `client_key`, `client_secret`, `tenant_id`, `email` |
| `quark` | 夸克网盘 | `cookie` |
| `yun139` | 天翼云盘 | `authorization` |
| `115` | 115 云盘 | `cookie` |
| `189` | 天翼云盘 189 | `cookie` |
| `webdav` | WebDAV | `url`, `username`, `password` |
| `s3` | Amazon S3 / 兼容 S3 | `bucket`, `endpoint`, `access_key_id`, `secret_access_key` |


## localfs

本地目录。

```toml
[mounts.params]
root_path = "/tmp/qrypt-remote"
```

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `root_path` | string | 是 | Local filesystem root directory path |

---

## aliyundrive

阿里云盘。

```toml
[mounts.params]
refresh_token = "your-refresh-token"
drive_id = "your-drive-id"
# root_path = "/qrypt"
# order_by = "name"
# order_direction = "ASC"
# api_base_url = "https://openapi.alipan.com"
# auth_url = "https://openapi.alipan.com/oauth/authorize"
```

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `refresh_token` | string (secret) | 是 | Aliyun Drive refresh token for OAuth authentication |
| `drive_id` | string | 是 | Aliyun Drive drive ID |
| `root_path` | string | 否 | Virtual root path, resolved to the provider folder ID at startup，默认 `/` |
| `order_by` | string | 否 | File listing sort field |
| `order_direction` | string | 否 | Sort direction (ASC or DESC) |
| `api_base_url` | string | 否 | Custom API base URL |
| `auth_url` | string | 否 | Custom OAuth token URL |

---

## baidu_netdisk

百度网盘。

```toml
[mounts.params]
refresh_token = "your-refresh-token"
# root_path = "/qrypt"
# order_by = "name"
# order_direction = "asc"
# use_online_api = true
# online_api = "https://api.oplist.org/baiduyun/renewapi"
# upload_api = "https://d.pcs.baidu.com"
# api_base_url = "https://pan.baidu.com/rest/2.0"
# oauth_url = "https://openapi.baidu.com/oauth/2.0/token"
# download_user_agent = "pan.baidu.com"
```

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `refresh_token` | string (secret) | 是 | Baidu Netdisk refresh token |
| `access_token` | string (secret) | 否 | Optional initial Baidu Netdisk access token; refreshed automatically when needed |
| `root_path` | string | 否 | Baidu Netdisk path used as this mount root，默认 `/` |
| `order_by` | string | 否 | List ordering field: name, time, or size，默认 `name` |
| `order_direction` | string | 否 | List ordering direction: asc or desc，默认 `asc` |
| `use_online_api` | boolean | 否 | Use OpenList-compatible online token refresh API，默认 `true` |
| `online_api` | string | 否 | Online token refresh API URL |
| `upload_api` | string | 否 | Baidu PCS upload API base URL |
| `client_id` | string (secret) | 否 | Baidu app API Key used as OAuth client_id when use_online_api=false |
| `client_secret` | string (secret) | 否 | Baidu app Secret Key used as OAuth client_secret when use_online_api=false |
| `api_base_url` | string | 否 | Custom Baidu REST API base URL |
| `oauth_url` | string | 否 | Custom Baidu OAuth token URL |
| `download_user_agent` | string | 否 | User-Agent used for Baidu download requests，默认 `pan.baidu.com` |

---

## onedrive

OneDrive。

```toml
[mounts.params]
refresh_token = "your-refresh-token"
# access_token = "optional-access-token"
# region = "global"
# root_path = "/qrypt"
# use_online_api = true
# online_api = "https://api.oplist.org/onedrive/renewapi"
# redirect_uri = "https://your-app/callback"
# api_base_url = "https://graph.microsoft.com"
# oauth_base_url = "https://login.microsoftonline.com"
# is_sharepoint = false
# site_id = "your-sharepoint-site-id"
# custom_host = "download.example.com"
# chunk_size = 5
# disable_disk_usage = false
```

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `refresh_token` | string (secret) | 是 | OneDrive refresh token |
| `access_token` | string (secret) | 否 | Optional initial OneDrive access token; refreshed automatically when needed |
| `region` | string | 否 | Microsoft cloud region，默认 `global` |
| `root_path` | string | 否 | OneDrive path used as this mount root，默认 `/` |
| `use_online_api` | boolean | 否 | Use OpenList-compatible online token refresh API，默认 `true` |
| `online_api` | string | 否 | Online token refresh API URL |
| `client_id` | string (secret) | 否 | OAuth client ID used when use_online_api=false |
| `client_key` | string (secret) | 否 | Alias for client_id |
| `client_secret` | string (secret) | 否 | OAuth client secret used when use_online_api=false |
| `redirect_uri` | string | 否 | OAuth redirect URI used when your Microsoft app requires it |
| `api_base_url` | string | 否 | Custom Microsoft Graph API base URL |
| `oauth_base_url` | string | 否 | Custom Microsoft OAuth base URL |
| `is_sharepoint` | boolean | 否 | Use SharePoint site drive instead of the current user's drive，默认 `false` |
| `site_id` | string | 否 | SharePoint site ID when is_sharepoint=true |
| `custom_host` | string | 否 | Custom host for download URLs |
| `chunk_size` | integer | 否 | Large upload chunk size in MiB，默认 `5` |
| `disable_disk_usage` | boolean | 否 | Disable OneDrive quota query，默认 `false` |

---

## onedrive_app

OneDrive 应用权限模式。这个驱动使用 Microsoft Entra 应用的 client credentials 访问指定用户的 OneDrive。

```toml
[mounts.params]
client_id = "your-client-id"
# client_key = "your-client-id"
client_secret = "your-client-secret"
tenant_id = "your-tenant-id"
email = "user@example.com"
# region = "global"
# root_path = "/qrypt"
# api_base_url = "https://graph.microsoft.com"
# oauth_base_url = "https://login.microsoftonline.com"
# custom_host = "download.example.com"
# chunk_size = 5
# disable_disk_usage = false
```

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `client_id` | string (secret) | 否 | Microsoft Entra application client ID；与 `client_key` 二选一 |
| `client_key` | string (secret) | 否 | Alias for client_id；与 `client_id` 二选一 |
| `client_secret` | string (secret) | 是 | Microsoft Entra application client secret |
| `tenant_id` | string | 是 | Microsoft Entra tenant ID |
| `email` | string | 是 | User principal name or email whose drive should be mounted |
| `region` | string | 否 | Microsoft cloud region，默认 `global` |
| `root_path` | string | 否 | OneDrive path used as this mount root，默认 `/` |
| `api_base_url` | string | 否 | Custom Microsoft Graph API base URL |
| `oauth_base_url` | string | 否 | Custom Microsoft OAuth base URL |
| `custom_host` | string | 否 | Custom host for download URLs |
| `chunk_size` | integer | 否 | Large upload chunk size in MiB，默认 `5` |
| `disable_disk_usage` | boolean | 否 | Disable OneDrive quota query，默认 `false` |

---

## quark

夸克网盘。

```toml
[mounts.params]
cookie = "k1=v1; k2=v2"
# root_path = "/qrypt"
# base_url = "https://drive.quark.cn"
# v2_url = "https://drive-m.quark.cn"
```

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `cookie` | string (secret) | 是 | Quark cloud drive authentication cookie |
| `root_path` | string | 否 | Virtual root path on the drive，默认 `/` |
| `base_url` | string | 否 | Custom API base URL |
| `v2_url` | string | 否 | Custom API v2 URL |

---

## yun139

天翼云盘。

```toml
[mounts.params]
authorization = "your-authorization-token"
# root_path = "/qrypt"
# root_id = "FtozqWiFB1yWOWUGc9oNCf6M0h5fRwcQl"
```

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `authorization` | string (secret) | 是 | 139 cloud drive authorization token |
| `root_path` | string | 否 | Virtual root path, resolved to the provider folder ID at startup，默认 `/` |
| `root_id` | string | 否 | Pre-resolved folder ID (skips root_path resolution) |

---

## 115

115 云盘。

```toml
[mounts.params]
cookie = "k1=v1; k2=v2"
# root_path = "/qrypt"
```

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `cookie` | string (secret) | 是 | 115 cloud drive authentication cookie |
| `root_path` | string | 否 | Virtual root path, resolved to the provider folder ID at startup，默认 `/` |

---

## 189

天翼云盘 189。

```toml
[mounts.params]
cookie = "k1=v1; k2=v2"
# root_path = "/qrypt"
# root_id = "-11"
```

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `cookie` | string (secret) | 是 | 189 cloud drive authentication cookie |
| `root_path` | string | 否 | Virtual root path on the drive，默认 `/` |
| `root_id` | string | 否 | Pre-resolved folder ID (skips root_path resolution) |

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

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `url` | string | 是 | WebDAV server base URL |
| `username` | string | 是 | WebDAV authentication username |
| `password` | string (secret) | 是 | WebDAV authentication password or app token |
| `root_path` | string | 否 | Optional path under the WebDAV base URL used as this mount root，默认 `/` |

---

## s3

Amazon S3 / 兼容 S3。

```toml
[mounts.params]
bucket = "my-bucket"
endpoint = "https://s3.us-east-1.amazonaws.com"
# region = "us-east-1"
access_key_id = "AKIA..."
secret_access_key = "..."
# custom_host = "cdn.example.com"
# force_path_style = true
# list_object_version = "v1"
# placeholder = ".qrypt"
# root_path = "/my-mount"
# sign_url_expire = "1h"
```

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `bucket` | string | 是 | S3 bucket name |
| `endpoint` | string | 是 | S3 endpoint URL |
| `region` | string | 否 | AWS region，默认 `us-east-1` |
| `access_key_id` | string (secret) | 是 | S3 access key ID |
| `secret_access_key` | string (secret) | 是 | S3 secret access key |
| `session_token` | string (secret) | 否 | S3 session token (for temporary credentials) |
| `custom_host` | string | 否 | Custom host for download URLs (e.g. CDN domain) |
| `force_path_style` | boolean | 否 | Force path-style addressing (required for MinIO and most non-AWS S3)，默认 `false` |
| `list_object_version` | string | 否 | S3 list API version: v1 or v2，默认 `v1` |
| `placeholder` | string | 否 | Placeholder filename for empty directories，默认 `.qrypt` |
| `root_path` | string | 否 | Root path prefix within the bucket，默认 `/` |
| `sign_url_expire` | string | 否 | Presigned URL expiration duration，默认 `4h` |
