---
name: qrypt-release
description: Manage qrypt version releases: cut tags, trigger CI builds, inspect release artifacts. Use when the user asks to release a new version, cut a tag, publish a release, or check CI release status.
---

# Qrypt Release

## Release Flow

1. **Merge to main** — ensure `feat/*` branch is merged into `main`.
2. **Tag and push** — create a semantic version tag and push:
   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```
3. **CI triggers automatically** — 3 parallel jobs:
   - `dist-linux-windows` (ubuntu-latest, Docker native): linux/amd64, linux/arm64, windows/amd64 (Docker), windows/arm64 (nocgo)
   - `dist-darwin` (macos-14, macfuse + native go build): darwin/amd64, darwin/arm64
   - `release` (after both dist jobs): goreleaser packages archives + checksums + changelog, publishes to GitHub Releases

## Build Matrix

| Target | Method | Deps |
|---|---|---|
| `linux/amd64` | Docker + fuse-dev | Docker |
| `linux/arm64` | Docker + fuse-dev + QEMU | Docker |
| `windows/amd64` | Docker + mingw + WinFSP headers | Docker |
| `windows/arm64` | nocgo cross-compile (pure Go) | none |
| `darwin/amd64` | native go build + FUSE | macOS |
| `darwin/arm64` | native go build + FUSE | macOS |

## Inspect CI

```bash
# List recent runs
gh run list --limit 5

# View workflow status
gh run view <run-id>

# View failed job logs
gh run view <run-id> --log-failed
```

## Version Convention

- `v0.0.1`, `v0.1.0`, `v1.0.0` — semantic versioning
- Tags must start with `v` to trigger CI (`tags: [v*]` in CI config)
- Changelog is auto-generated from conventional commits (`feat:`, `fix:`) grouped by type
