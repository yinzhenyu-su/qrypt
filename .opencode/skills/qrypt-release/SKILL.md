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
3. **CI triggers automatically**:
   - `check`: runs vet and tests before release builds.
   - `dist-linux-windows`: builds Linux binaries with Docker and Windows binaries with nocgo cross-compilation, then packages release archives.
   - `dist-darwin`: builds macOS binaries on macOS with macFUSE installed, then packages release archives.
   - `release`: downloads packaged archives, then GoReleaser publishes GitHub Release assets, checksums, and changelog.

Release assets are packaged before GoReleaser runs. `.goreleaser.yaml` uses `builds.skip: true` and `release.extra_files`, so GoReleaser is only responsible for publishing prebuilt archives, generating checksums, and generating release notes.

## Build Matrix

| Target | Method | Deps |
|---|---|---|
| `linux/amd64` | Docker + fuse-dev | Docker |
| `linux/arm64` | Docker + fuse-dev on `ubuntu-24.04-arm` | Docker |
| `windows/amd64` | nocgo cross-compile | Go only |
| `windows/arm64` | nocgo cross-compile | Go only |
| `darwin/amd64` | native go build + FUSE | macOS |
| `darwin/arm64` | native go build + FUSE | macOS |

Windows builds use `CGO_ENABLED=0 -tags nocgo`. They still require users to install WinFSP at runtime for mount support; nocgo only removes the CI dependency on mingw and WinFSP headers.

## Release Assets

Expected GitHub Release assets:

- `qrypt_<version>_linux_amd64.tar.gz`
- `qrypt_<version>_linux_arm64.tar.gz`
- `qrypt_<version>_darwin_amd64.tar.gz`
- `qrypt_<version>_darwin_arm64.tar.gz`
- `qrypt_<version>_windows_amd64.zip`
- `qrypt_<version>_windows_arm64.zip`
- `qrypt_<version>_checksums.txt`

If a release only contains GitHub's default source archives, check that:

- CI uploaded packaged artifacts from `dist/*.tar.gz` and `dist/*.zip`.
- The release job downloaded artifacts into `dist/`.
- `.goreleaser.yaml` has matching `release.extra_files` and `checksum.extra_files` globs.
- The release job passes `GITHUB_TOKEN: ${{ secrets.GH_TOKEN }}` and has `contents: write` permission.

The release config uses `replace_existing_artifacts: true`, so rerunning the same tag can replace uploaded assets.

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
