# Changelog

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
