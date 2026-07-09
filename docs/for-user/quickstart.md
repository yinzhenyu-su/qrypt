# qrypt 快速上手

本文档将带你用最小的配置在 5 分钟内跑通 qrypt，直观感受它的工作方式。

## 简介

qrypt 是一个加密虚拟文件系统。它把云盘挂载到你电脑上的一个本地文件夹里，每个云盘对应这个文件夹下的一个子目录。

支持多种云盘后端，文件在传输和存储时可以进行 rclone 兼容的加密。

## 下载与安装

从 [releases 页面](https://github.com/yinzhenyu-su/qrypt/releases) 下载对应系统的压缩包：

| 系统                  | 文件名                               |
| --------------------- | ------------------------------------ |
| macOS (Intel)         | `qrypt_`_版本_`_darwin_amd64.tar.gz` |
| macOS (Apple Silicon) | `qrypt_`_版本_`_darwin_arm64.tar.gz` |
| Linux (amd64)         | `qrypt_`_版本_`_linux_amd64.tar.gz`  |
| Linux (arm64)         | `qrypt_`_版本_`_linux_arm64.tar.gz`  |
| Windows (amd64)       | `qrypt_`_版本_`_windows_amd64.zip`   |
| Windows (arm64)       | `qrypt_`_版本_`_windows_arm64.zip`   |

解压后得到一个名为 `qrypt` 的可执行文件（Windows 上为 `qrypt.exe`）。

你可以把它放到 `PATH` 中的某个目录，也可以直接在文件所在目录下运行。

验证安装：

```
./qrypt --help
```

> **Windows**：使用 `.\qrypt.exe --help`，或放入 `PATH` 后直接运行 `qrypt --help`。

## 编写配置文件

qrypt 需要一个配置文件来知道挂载到哪里、接入哪些云盘。

创建一个名为 `qrypt.toml` 的文件，写入以下内容：

```toml
mount_point = "~/Qrypt"

[[mounts]]
name = "local"
type = "localfs"

[mounts.params]
root = "/tmp/qrypt-data"

[mounts.encryption]
password = "my-password"
filename_encryption = "standard"
filename_encoding = "base32"
```

这个配置文件定义了一个云盘，指向本机的 `/tmp/qrypt-data` 目录，并开启了加密。

> **Windows**：将 `root` 改为本地的一个目录，例如：
>
> ```toml
> root = "C:\\qrypt-data"
> ```

每个配置项的含义：

- `mount_point` —— 文件管理器里看到的挂载目录，所有云盘都会出现在这个文件夹里
- `[[mounts]]` —— 每条 mounts 配置对应一个云盘服务，可以写多条
- `name` —— 这个云盘在挂载点下的子目录名称
- `type` —— 云盘类型，这里是 `localfs`（本地目录）
- `root` —— 该云盘后端存储的实际路径
- `[mounts.encryption]` —— 该云盘的加密配置，开启后文件名和文件内容会被加密

## 准备演示目录

在本地创建一个目录，用来模拟云盘后端：

| 系统          | 命令                       |
| ------------- | -------------------------- |
| macOS / Linux | `mkdir -p /tmp/qrypt-data` |
| Windows       | `mkdir C:\qrypt-data`      |

## 查看文件系统

运行以下命令，qrypt 会读取配置文件并展示文件系统的顶层结构：

```
./qrypt fs list /
```

> **Windows**：`.\qrypt.exe fs list /`

你应该能看到类似这样的输出：

```
local/
```

云盘已就绪，`local` 目录下的文件会被加密存储。

## 上传和读取文件

先创建一个测试文件：

| 系统          | 命令                                  |
| ------------- | ------------------------------------- |
| macOS / Linux | `echo "hello qrypt" > /tmp/hello.txt` |
| Windows       | `echo hello qrypt > C:\hello.txt`     |

上传到 qrypt 文件系统中：

| 源文件（根据上一步调整） | 命令                                               |
| ------------------------ | -------------------------------------------------- |
| macOS / Linux            | `./qrypt fs put /tmp/hello.txt /local/hello.txt`   |
| Windows                  | `.\qrypt.exe fs put C:\hello.txt /local/hello.txt` |

读取刚才上传的文件：

```
./qrypt fs cat /local/hello.txt
```

终端会输出：

```
hello qrypt
```

查看云盘中的文件列表：

```
./qrypt fs list /local
```

## 查看加密效果

现在看一下后端目录里实际存储了什么：

| 系统          | 命令                 |
| ------------- | -------------------- |
| macOS / Linux | `ls /tmp/qrypt-data` |
| Windows       | `dir C:\qrypt-data`  |

你可能看不到 `hello.txt`，而是一个类似 `b4l6gr1s6t1q0tas6dl0q0mb0s62kbj0` 的无意义文件名。用 cat 查看这个文件的内容：

| 系统          | 命令                                                   |
| ------------- | ------------------------------------------------------ |
| macOS / Linux | `cat /tmp/qrypt-data/b4l6gr1s6t1q0tas6dl0q0mb0s62kbj0` |
| Windows       | `type C:\qrypt-data\b4l6gr1s6t1q0tas6dl0q0mb0s62kbj0`  |

输出是一串二进制乱码。即使他人拿到了后端文件，也无法读取原始内容。

> 注意：你的加密文件名很可能和上面不同，因为 salt 是随机生成的。

## 挂载为本地文件夹

前面的操作没有挂载也能进行。如果你想通过文件管理器直接操作，可以挂载它：

```
./qrypt mount
```

> **macOS 用户**：需要先安装 [macFUSE](https://macfuse.io/) 才能使用挂载功能。
>
> **Windows 用户**：需要先安装 [WinFsp](https://winfsp.dev/) 才能使用挂载功能。

挂载成功后，打开文件管理器并进入 `~/Qrypt`，你会看到一个 `local` 文件夹。你可以像操作普通文件夹一样拖入文件、双击打开、删除文件。所有操作都会被 qrypt 处理后转发到后端目录，文件在存储时会被加密。

## qrypt 的工作方式

几句话了解 qrypt 的核心概念：

- **挂载点（mount_point）**：所有云盘汇聚到一个本地目录。每个云盘是挂载点下的一个子目录。
- **驱动（mounts）**：每条 `[[mounts]]` 配置对应一个云盘服务。qrypt 内置了多种云盘驱动，这里用的 `localfs` 是最简单的一种。
- **fs 命令**：即使不挂载，也能用 `fs list`、`fs put`、`fs cat`、`fs get` 等命令直接操作文件。这在脚本和远程环境中尤其方便。

## 下一步

你已经体验了 qrypt 的基本流程。接下来可以：

- 换成真实云盘（[支持的驱动列表](support-drivers.md)）——把 `type` 改为 `aliyundrive`、`quark`、`yun139`、`webdav`、`s3` 或 `baidu_netdisk`，并填写对应的认证参数
- 了解其他配置项（[完整配置参考](../README.md)）——缓存、带宽控制、日志等
- 配置开机自动挂载
