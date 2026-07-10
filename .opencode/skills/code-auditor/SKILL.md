---
name: code-auditor
description: Periodic code health maintenance for the qrypt project. Scans the codebase for naming inconsistencies, duplicated code, package structure issues, and domain concept drift. Generates a structured report with fix suggestions. Use when the user asks to audit code, review code health, run maintenance, check for technical debt, or enforce coding conventions.
---

# Code Auditor

Analyze the qrypt codebase for structural and naming issues, then produce a maintenance report. This skill is designed for periodic use (e.g. weekly) to counteract the drift that occurs when AI agents make changes with limited project context.

## Workflow

### Phase 0: Load or Initialize Knowledge Base

Load `CODEMAP.md` and `codemap-data.json` from the project root.
If they do not exist, run Phase 0 to bootstrap them before proceeding.

**Phase 0 — Bootstrap (first run only):**

1. Scan all Go packages with `go list -json ./...` to build the package dependency graph.
2. For each package, use AST-grep (`ast_grep_search`) or grep to extract:
   - Exported type definitions (`type X struct` / `type X interface`)
   - Exported function signatures (`func X(`)
   - Constants and variables (`const X` / `var X`)
   - File naming patterns (`snake_case.go`, `kebab_case.go`)
3. Infer naming conventions from the extracted data:
   - Variable/function naming style (camelCase, PascalCase, snake_case)
   - Package naming style
   - Common abbreviation rules
4. Write the bootstrap results:

   **`codemap-data.json`** — structured data for machine consumption:

   ```json
   {
     "generated_at": "2026-07-10T12:00:00+08:00",
     "packages": {
       "pkg/drive": { "path": "github.com/...", "deps": [...], "files": ["drive.go",...] }
     },
     "symbols": {
       "formatBytes": { "kind": "func", "pkg": "osutil", "file": "osutil/format.go", "signature": "func(int64) string" }
     },
     "types": {
       "Entry": { "kind": "struct", "pkg": "drive", "file": "drive/drive.go", "fields": ["ID","ParentID","Name","IsDir","Size","ModTime","Extra"] }
     },
     "naming": {
       "function_style": "camelCase",
       "type_style": "PascalCase",
       "package_style": "snake_case",
       "file_style": "snake_case",
       "common_abbreviations": ["HTTP", "URL", "ID", "API", "JSON", "SHA1", "MD5", "FUSE", "OSS", "VFS"]
     }
   }
   ```

   **`CODEMAP.md`** — human-readable conventions document:

   ```markdown
   # Code Map — qrypt

   ## Naming Conventions

   - 函数/方法: camelCase (小写动词开头，如 `list`, `resolvePath`)
   - 类型/接口: PascalCase
   - 包名: snake_case
   - 文件名: snake_case.go

   ## Domain Concepts

   - `Entry`: 文件系统条目 (pkg/drive/drive.go)
   - `Driver`: 云存储驱动接口 (pkg/drive/drive.go)
   - `VFS`: 虚拟文件系统 (pkg/vfs/vfs.go)

   ## Package Architecture

   - `pkg/drive`: 核心抽象（Driver, Entry, SourceUploader 等接口）
   - `pkg/vfs`: 虚拟文件系统层（缓存、staging、写回）
   - `internal/driver/*`: 各存储驱动实现
   - `internal/cli`: CLI 命令入口
   - `internal/control`: Debug socket 服务端
   - `pkg/crypt`: 加密层

   ## Known Anti-patterns

   - 不要直接依赖具体 driver 类型，通过 `drive.Driver` 接口
   - 不要在 driver 中引入 VFS 概念（cache、staging）
   - 密码/令牌类参数标记为 `secret: true`
   ```

### Phase 1: Scan

Run the following checks in parallel where possible:

#### 1a. Naming Audit

Compare current code against `codemap-data.json` naming conventions.
Use `ast_grep_search` with language=go to find:

```yaml
# Functions that don't match camelCase
pattern: func $NAME($$$) { $$$ }
```

- Flag any exported function where the first letter is lowercase (mismatch with Go export rules)
- Flag any method where the name doesn't match the inferred style (e.g. `ListFile` when convention is `list`)
- Cross-reference against known symbols in `codemap-data.json` to identify duplicate definitions

#### 1b. Duplication Audit

For each package, extract all function and constant definitions.
Compare across packages:

- Identical function bodies detected with AST-grep pattern matching
- Structurally similar code (same pattern with different types/variables)
- Duplicate constants or utility functions (e.g. same `formatBytes` in multiple drivers)

Use `ast_grep_search` with patterns like:

```yaml
# Find all error-sentinel patterns
pattern: var $NAME = fmt.Errorf($$$)
```

#### 1c. Package Structure Audit

Run `go list -json ./...` and analyze:

- Packages that import concrete driver types instead of interfaces
- Packages with too many files (>10 is a warning, >20 is an error)
- Unused/unreachable packages
- Files that belong to the wrong package based on their content

#### 1d. Domain Concept Audit

For each key domain type in `codemap-data.json`:

- Find all usages across the codebase (grep for the type name)
- Check if the same concept is defined in multiple places
- Flag inconsistent field names or types for the same concept

### Phase 2: Report

Generate a Markdown report at the project root: `codereport-YYYY-MM-DD.md`

```markdown
# Code Audit Report — 2026-07-10

## Summary

- 扫描文件: 127
- 发现问题: 12 (error: 3, warning: 7, info: 2)

## 命名问题 (4)

### 函数命名不一致

| 文件            | 当前名     | 建议名 | 依据               |
| --------------- | ---------- | ------ | ------------------ |
| `driver.go:142` | `ListFile` | `list` | 方法名动词小写开头 |

## 重复代码 (3)

### 重复的工具函数

| 符号          | 位置 1              | 位置 2               | 建议                |
| ------------- | ------------------- | -------------------- | ------------------- |
| `formatBytes` | `p115/driver.go:88` | `quark/driver.go:95` | 提取到 `pkg/osutil` |

## 包结构问题 (2)

...

## 领域概念不一致 (3)

...

## 修复建议

以上问题可通过以下步骤修复：

1. ...
```

Append any unreported findings to `codemap-data.json` as new entries, so future runs benefit from accumulated knowledge.

### Phase 3: Update Knowledge Base

After generating the report, update `codemap-data.json`:

- Add any newly discovered symbols to the `symbols` index
- Add any newly discovered types to the `types` index
- Do NOT overwrite `CODEMAP.md` conventions (those are human-curated)

## Tool Selection Guide

| Check                | Tool                                    | Reason                                   |
| -------------------- | --------------------------------------- | ---------------------------------------- |
| 提取类型/函数/常量   | `ast_grep_search` (go)                  | AST-level, handles generics and comments |
| 包依赖分析           | `go list -json ./...` via bash          | 标准工具，信息最全                       |
| 文本搜索（类型引用） | `grep`                                  | 跨文件文本查找                           |
| 重复代码检测         | `ast_grep_search` + manual comparison   | AST pattern matching + review            |
| Lint 风格检查        | `golangci-lint` (if available) via bash | 自动化风格检查                           |

## Output Files

| 文件                       | 位置                  | 用途                         |
| -------------------------- | --------------------- | ---------------------------- |
| `CODEMAP.md`               | 项目根/.code-auditor/ | 人类可读的代码约定和领域知识 |
| `codemap-data.json`        | 项目根/.code-auditor/ | 机器可读的结构化索引         |
| `codereport-YYYY-MM-DD.md` | 项目根/.code-auditor/ | 每次运行的检查报告           |

## Quality Checklist

Before concluding the audit:

- [ ] All four audit dimensions completed (naming, duplication, package, domain)
- [ ] `codemap-data.json` updated with new discoveries
- [ ] Report file written to project root
- [ ] Report includes actionable fix suggestions
- [ ] No false positive noise in error-severity items
