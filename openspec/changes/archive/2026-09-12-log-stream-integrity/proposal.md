## Why

日志落盘目前是**互斥分流**的：`pkg/logging/log.go:223-227` 用 `if level >= LevelWarn && errWriter != nil { errWriter } else if writer != nil { writer }` 决定去向，于是主日志只写 info/debug，错误日志只写 warn/error。而这个仓库的两条启动路径都会把两个文件配成不同路径——`pkg/core/core.go:730-741` 固定使用 `qrypt.log` 与 `qrypt-error.log`，`internal/cli/runtime_config.go:36-43` 从配置推导两个路径。结果是 `tail qrypt.log` **一行错误都看不到**：用户看主日志会得出"一切正常"的结论，事故复盘必须手工交织两个文件并按时间戳对齐。对一个"盘不工作"时最常被打开的产物来说，这是主动误导而非信息缺失。

第二处：日志器的安装方式有两条不一致的路径。`pkg/core/core.go:743` 调 `logging.ReplaceDefault`（原地改写全局 `L` 的字段、关闭旧的 lumberjack 句柄、加锁）；而 `internal/cli/runtime_config.go:51` 直接 `logging.L = newLogger`——**指针重写**。后者（a）泄漏上一个 logger 的文件句柄（旧 `lumberjack.Logger` 永不 Close，滚动的压缩与清理停摆），（b）与并发写日志的 goroutine 构成对全局变量的无同步写，`go test -race` 之外的运行期属于数据竞争，（c）使任何已捕获 `logging.L` 指针的位置改写后仍指向旧 logger。日志是否可信取决于启动路径，这本身不可接受。

第三处：`sanitize()`（`pkg/logging/log.go:21-34`）的敏感串黑名单只有 ctoken/__puus/__kp/__kps/password/salt/cookie 七类，缺少通用令牌参数名。而 `pkg/drivers/quark/quark_upload.go:63` 在 **Info** 级别就打印了 multipart 断点续传凭据 `upload_id=%q`；`token=`/`access_token=`/`refresh_token=`/`Authorization:`/`OSSAccessKeyId=`/`Signature=` 也都不在名单内，只因为当前没有别的地方把它们写进日志而未暴露。日志会被 `qrypt debug bundle` 打包发给维护者（这也是 bundle 唯一携带的持久信号），因此"能不能安全分享"和"完不完整"是同一个问题。

现在做的理由：这三处都属于**日志流本身的可靠性**，改动集中在 `pkg/logging` 与两处启动代码，成本极低；而它们是后续任何日志侧能力（关联、聚合、告警）的地基——地基不可信时，往上加东西只会放大误导。

## What Changes

- **主日志包含全部级别，错误日志是它的 warn+ 子集**：warn/error 同时写入主日志与错误日志；当两个 sink 是同一个 writer（未配置文件输出时都是 stderr）时只写一次，不产生重复行。错误日志保持"只看错误"的用途，主日志成为可单独按时间顺序阅读的完整记录。
- **日志器只有一种安装方式**：`internal/cli/runtime_config.go` 改用 `logging.ReplaceDefault`，与 `pkg/core` 一致；据此获得"安装后旧 writer 被关闭"与"全局指针在安装前后保持同一对象"两条可断言性质。
- **敏感串黑名单补齐通用令牌参数名**：新增 `upload_id=`/`uploadId=`、`access_token=`、`refresh_token=`、`token=`、`Authorization:`、`OSSAccessKeyId=`、`Signature=` 的值掩码，使 quark 的 Info 级 `upload_id=` 行不再泄漏续传凭据；掩码在写入任何 sink 之前生效，因此主日志与错误日志同等受保护。
- **不加开关、不改日志格式**：行格式、级别语义、采样行为、事件环形缓冲与 `/v1/events` 契约全部不变。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `observability`: 新增三条关于持久日志流的要求——主日志必须包含全部级别且错误日志是其 warn+ 子集；日志器必须以单一路径安装且安装后旧的输出目标被释放；写入任何输出目标之前必须对已知令牌参数名做掩码。既有要求（读事件历史保留）不变。

## Impact

- `pkg/logging/log.go`：三处写行分支（`logf`、`logfEvery`、`logEveryFunc`）的 sink 选择；`sensitivePatterns` 名单。
- `internal/cli/runtime_config.go`：日志器安装改为 `ReplaceDefault`。
- 测试：`pkg/logging/log_test.go` 新增主日志完整性、单 sink 不重复、掩码覆盖用例；`internal/cli` 侧断言安装后旧 writer 被关闭且全局指针不变。
- 磁盘影响：warn/error 现在会写两份（主日志 + 错误日志）。warn 以上相对 info/debug 稀疏，且这一重复正是"主日志可单独阅读"的代价。
- 无配置项、无 wire 字段、无 CLI 行为改动；`debug bundle` 打包内容与格式不变。
