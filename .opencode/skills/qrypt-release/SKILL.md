---
name: qrypt-release
description: Cut and verify a qrypt release. Use when asked to release a version, bump the version, cut or push a tag, rerun a failed release, or verify release assets, checksums, changelog, or GitHub Release notes.
---

# Qrypt Release

## Preconditions

- Work on `main`, clean tree, synced with `origin/main`.
- Go version matches `.github/workflows/ci.yaml` (`1.27.4` at the time of writing).
- `gh auth status` succeeds; `gh repo view --json nameWithOwner` points at the
  qrypt repository.
- Local CI passed before the release commit:
  ```bash
  scripts/ci/check-all.sh
  ```
  It must end with `== local CI check passed ==`.
- `CHANGELOG.md` 顶部的 `## Unreleased` 用于手工内容（`### Thanks`、迁移提示等）。
  `gen-changelog.sh` 会把它并入新版本并删除该段；不要把这些内容写到别处。

## Version choice

- Patch (`v0.6.1`): bug fixes only.
- Minor (`v0.7.0`): new features, backward compatible.
- Major (`v1.0.0`): breaking changes.
- Tags must start with `v`; CI only triggers on `v*`.

## Preferred flow

`release.sh` generates the changelog, commits it, and creates the tag. It does
**not** push.

```bash
git switch main
git pull --ff-only origin main
scripts/ci/check-all.sh
scripts/release/release.sh vX.Y.Z
git push origin main
git push origin vX.Y.Z
```

The release script:

1. checks clean tree, tag absence, current branch `main`;
2. runs `scripts/release/gen-changelog.sh vX.Y.Z` (commits from the previous
   tag, grouped into Features / Bug fixes / Others, plus the folded
   `Unreleased` block);
3. commits `docs: update changelog for vX.Y.Z` when the changelog changed;
4. creates the local tag.

If `main` is protected against direct pushes, push the changelog through a PR
first, merge it, then tag the merged commit and push only the tag. Do not tag a
commit that is not on `origin/main`.

### Manual fallback

```bash
scripts/release/gen-changelog.sh vX.Y.Z
git add CHANGELOG.md
git commit -m 'docs: update changelog for vX.Y.Z'
git tag vX.Y.Z
git push origin main
git push origin vX.Y.Z
```

## What the tag push runs

| Job | Purpose |
|---|---|
| `check` | static, fast, race, stability suites |
| `update-changelog` | creates `changelog/vX.Y.Z` PR only if the tagged CHANGELOG lacks the version; skipped when already present |
| `dist-linux-windows` | Linux (Docker + fuse-dev) and Windows (nocgo) archives |
| `dist-darwin` | macOS amd64/arm64 with macFUSE |
| `windows-release-smoke` | installs WinFsp and mounts with the released Windows amd64 binary |
| `release` | GoReleaser publishes prebuilt archives, checksums, and release notes; then appends `### Thanks` from CHANGELOG |

Build matrix and packaging details live in `.github/workflows/ci.yaml` and
`.goreleaser.yaml`.

## Post-release verification

```bash
# run id for the tag
gh run list --branch vX.Y.Z --limit 3

# follow the whole workflow
gh run watch <run-id> --exit-status
gh run view <run-id> --json status,conclusion,jobs \
  --jq '{status,conclusion,jobs:[.jobs[]|{name,conclusion}]}'

# release page and assets
gh release view vX.Y.Z --json tagName,isDraft,isPrerelease,publishedAt,url,assets \
  --jq '{tag:.tagName,isDraft,isPrerelease,publishedAt,url,assets:[.assets[]|{name,size}]}'

# notes must include the expected groups and Thanks block
gh release view vX.Y.Z --json body --jq .body

# main CI for the changelog commit
gh run list --branch main --limit 2
```

Expected assets:

- `qrypt_<version>_linux_amd64.tar.gz`
- `qrypt_<version>_linux_arm64.tar.gz`
- `qrypt_<version>_darwin_amd64.tar.gz`
- `qrypt_<version>_darwin_arm64.tar.gz`
- `qrypt_<version>_windows_amd64.zip`
- `qrypt_<version>_windows_arm64.zip`
- `qrypt_<version>_checksums.txt`
- `install.sh`, `install.ps1`

## Failure handling

- **Tag already exists**: delete and recreate only when intentional
  (`git tag -d`, `git push origin :refs/tags/vX.Y.Z`), or use the
  `source-command-git-retag` skill. Rerunning the same tag via
  `workflow_dispatch` is also supported:
  ```bash
  gh workflow run ci.yaml -f version=vX.Y.Z
  ```
  `replace_existing_artifacts: true` replaces uploaded assets.
- **Release has only source archives**: the release job did not receive
  packaged artifacts. Check `dist/*.tar.gz` / `dist/*.zip` upload in the dist
  jobs and the `prebuilt/qrypt_*` globs in `.goreleaser.yaml`.
- **`update-changelog` opened a PR**: the tagged commit's CHANGELOG did not
  contain the version. Merge the PR after the release workflow finishes.
- **Release notes miss `### Thanks`**: verify the tagged `CHANGELOG.md` has a
  `### Thanks` block (or `## Unreleased` with one), then rerun
  `scripts/release/add-release-thanks.sh vX.Y.Z`; it is idempotent.
- **Own PR is blocked by review**: GitHub does not allow approving your own PR.
  As a maintainer, either have another reviewer approve it or use
  `gh pr merge <n> --squash --admin` only after all required checks pass.
  Never bypass a failing check.

## Safety

- Never print cookies, tokens, app passwords, signed URLs, or full
  `qrypt.toml` credentials in logs or PR bodies.
- Do not hand-edit version strings in code; the version comes from the tag via
  `-ldflags`.
- Do not upload release assets manually; let CI own the asset set.
