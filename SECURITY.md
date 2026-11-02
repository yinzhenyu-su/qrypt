# Security Policy

## Supported versions

qrypt 是个人维护的开源项目。安全修复优先进入最新正式版本；旧版本只做 best-effort 修复，不承诺长期支持。

| Version | Supported |
| --- | --- |
| 最新 release | ✅ |
| 旧版本 | best effort |

## Reporting a vulnerability

请不要在公开 Issue、Discussion 或 PR 中披露漏洞细节、利用代码、真实凭证或用户数据。

优先使用 GitHub 的私密漏洞报告：

https://github.com/yinzhenyu-su/qrypt/security/advisories/new

如果该入口不可用，请通过维护者 GitHub 主页上的联系方式私下联系，并在标题中注明 `qrypt security`。

报告时请尽量包含：

- 受影响版本和平台
- 受影响的模块（mount、fs、crypt、driver、installer、upgrade 等）
- 复现步骤和影响
- 是否需要真实云盘账号
- 脱敏后的日志或配置片段

不要提供真实 cookie、token、密码、私钥或完整用户配置；用 `<redacted>` 代替。

## Scope

重点关注：

- 凭证存储、刷新和日志泄露
- 路径解析、挂载命名空间和越界访问
- 加密/解密、文件名编码和密钥派生
- 安装脚本、升级流程和发布产物完整性
- 驱动对远端响应的信任边界

## What to expect

维护者会尽快确认收到报告，并在修复可用后协调披露。由于是个人维护项目，不承诺固定 SLA。
