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
3. **CI triggers automatically** — GitHub Actions runs `dist` job on `macos-14`:
   - Installs Docker via colima
   - Runs `make -j6 dist` (6 platform binaries)
   - goreleaser packages archives + checksums + changelog
   - Publishes to GitHub Releases

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
