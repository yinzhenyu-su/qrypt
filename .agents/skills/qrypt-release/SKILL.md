---
name: qrypt-release
description: Entry point for cutting and verifying a qrypt release. Use when asked to release a version, bump the version, cut or push a tag, recover a failed release, or verify release assets, checksums, changelog, or GitHub Release notes.
---

# Qrypt Release

## Red lines

1. **No tag until the changelog is on `origin/main`.** Land the changelog commit
   through a PR (`gh pr merge <n> --squash --delete-branch --admin` after the six
   required checks), re-tag the merged commit, then push only the tag. Never tag
   a commit outside `origin/main`; `verify` rejects it.
2. **Publishing is `release.yaml` only.** Tag push or
   `gh workflow run release.yaml -f version=vX.Y.Z`; `scripts/release/release.sh`
   only commits the changelog and creates a local tag — it never pushes or
   publishes.
3. **Never bypass the `verify` gate or a failing required check.** A tag is
   releasable only when its commit is reachable from `main` and green (directly,
   or via a merged PR whose head tree matches). Make the commit green first.
4. **Flatten the generated changelog before merging.** `gen-changelog.sh` folds
   the raw `## Unreleased` block (with its own Features/Bug fixes/Others
   headings) into the new version and can duplicate hand-written bullets; keep a
   single set of sections, then amend and move the tag.
5. **No secrets, no hand-made assets.** Never print cookies/tokens/credentials,
   do not hand-upload release assets, and do not hand-edit version strings — the
   version comes from the tag.

## Workflow layout

| Workflow | Scope |
|---|---|
| `.github/workflows/ci.yaml` | tests only: PRs targeting `main` and pushes to `main`. Tag pushes no longer run it. |
| `.github/workflows/release.yaml` | release only: `push` of a `v*` tag plus `workflow_dispatch` (`version`). Jobs: `verify`, `dist-linux-windows`, `dist-darwin`, `windows-release-smoke`, `release`. |

Consequences:

- Recover a failed release with `gh workflow run release.yaml -f version=vX.Y.Z`;
  there is no release path through `ci.yaml` anymore.
- `scripts/release/release.sh` only does the local half: changelog commit +
  local tag. It never pushes and never publishes.
- `verify` is the safety gate. The tagged commit must be reachable from
  `origin/main`, and either its required checks are green (the `PR title` check
  may be skipped on a non-PR commit), or the head commit of its merged PR has
  the same tree and all six checks green. This closes the gap that any repo
  writer can create a tag: a tag only releases when `main` already contains a
  green commit. If `verify` fails, make the commit green first (re-run its main
  CI run or land it through a PR), then dispatch again; never bypass the gate.

## Preconditions

- Work on `main`, clean tree, synced with `origin/main`.
- The Go version is single-sourced from `go.mod`: the workflows use
  `go-version-file: go.mod` and `scripts/ci/check-all.sh` parses the same file.
  Do not keep a second copy.
- `gh auth status` succeeds; `gh repo view --json nameWithOwner` points at the
  qrypt repository.
- The target `main` SHA is green before you start. Check it, and let a
  Nightly Quality Gates run finish first when the change is risky:

  ```bash
  repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
  git fetch origin main
  sha="$(git rev-parse origin/main)"
  gh api "repos/$repo/commits/$sha/check-runs" \
    --jq '[.check_runs[]|{name,status,conclusion}]'

  git switch main
  git pull --ff-only origin main
  scripts/ci/check-all.sh     # must print "== local CI check passed =="
  ```

- `CHANGELOG.md` 顶部的 `## Unreleased` 用于手工内容（`### Thanks`、迁移提示等）。
  `gen-changelog.sh` 会把它并入新版本并删除该段；不要把这些内容写到别处。

## Version choice

- Patch (`v0.6.1`): bug fixes only.
- Minor (`v0.7.0`): new features, backward compatible.
- Major (`v1.0.0`): breaking changes.
- Tags must start with `v`; only `v*` triggers `release.yaml`.

## Preferred flow (`main` is protected)

1. Start from a green, up-to-date `main` (see Preconditions).
2. Run the local release step (changelog commit + local tag, no push):

   ```bash
   scripts/release/release.sh vX.Y.Z
   ```

   The script checks the clean tree, tag absence and current branch, runs
   `gen-changelog.sh`, commits `docs: update changelog for vX.Y.Z` when the
   changelog changed, and creates the local tag.
3. Flatten the generated changelog (see "Changelog quality"), then amend the
   commit and move the tag onto the amended commit. If `release.sh` reported
   that the changelog did not change (no commit was created), skip the amend:

   ```bash
   git add CHANGELOG.md
   git commit --amend --no-edit
   git tag -f vX.Y.Z
   ```

4. Land the changelog commit through a PR — direct pushes to `main` are
   blocked:

   ```bash
   git switch -c release/vX.Y.Z-changelog
   git push -u origin release/vX.Y.Z-changelog
   gh pr create --base main --title 'docs: update changelog for vX.Y.Z' \
     --body 'Changelog for the vX.Y.Z release.'
   gh pr checks <n> --watch
   ```

   Wait for all six required checks: `Check (static)`, `Check (fast)`,
   `Check (race)`, `Check (stability)`, `Test (windows)`, `PR title`.
5. Merge the PR. Your own PR cannot be approved by you, so use `--admin` once
   the required checks above are green:

   ```bash
   gh pr merge <n> --squash --delete-branch --admin
   ```

6. Re-tag on the **merged** commit and push only the tag:

   ```bash
   git switch main
   git fetch origin main
   git reset --hard origin/main    # local main still carries the pre-squash commit
   git tag -f vX.Y.Z
   git merge-base --is-ancestor vX.Y.Z origin/main   # must exit 0 before pushing
   git push origin vX.Y.Z
   ```

Rules:

- Never push `main` directly and never push the tag before the changelog merge.
- Never tag a commit that is not on `origin/main`; `verify` rejects it and it is
  the wrong content anyway.
- The tag push is the only thing that starts the release; do not run dist jobs
  by hand.

### Manual fallback

If `release.sh` is unavailable, do the same steps by hand and continue from
step 3 above:

```bash
scripts/release/gen-changelog.sh vX.Y.Z
git add CHANGELOG.md
git commit -m 'docs: update changelog for vX.Y.Z'
git tag vX.Y.Z
```

## Changelog quality

`gen-changelog.sh` appends the raw `## Unreleased` block — including its own
`### Features` / `### Bug fixes` / `### Others` headings — under the new
version heading, and generated bullets can duplicate the hand-written ones.
After `release.sh`, edit `CHANGELOG.md` before pushing the PR:

- keep exactly one `### Features`, one `### Bug fixes`, one `### Others`
  (when non-empty) under the new version;
- drop generated entries already covered by a hand-written `## Unreleased`
  entry;
- keep migration notes and `### Thanks` under their own headings;
- amend the commit and move the tag (`v0.7.0` was flattened from `058dded` to
  `94f738c`).

## What the tag push runs

| Job | Purpose |
|---|---|
| `verify` | gate: tag commit reachable from `origin/main` and green (see above) |
| `dist-linux-windows` | Linux (Docker + fuse-dev) and Windows (nocgo) archives |
| `dist-darwin` | macOS amd64/arm64 with macFUSE |
| `windows-release-smoke` | installs WinFsp and mounts with the released Windows amd64 binary |
| `release` | GoReleaser publishes prebuilt archives, checksums, and release notes; then appends `### Thanks` from CHANGELOG |

Build matrix and packaging details live in `.github/workflows/release.yaml` and
`.goreleaser.yaml`.

## Post-release verification

The release page body and `CHANGELOG.md` are two sources: GoReleaser builds the
notes from commits, not from `CHANGELOG.md`. Verify both.

```bash
# the release run for the tag
gh run list -w release.yaml -L 3 \
  --json databaseId,event,headSha,status,conclusion
gh run watch <run-id> --exit-status
gh run view <run-id> --json status,conclusion,jobs \
  --jq '{status,conclusion,jobs:[.jobs[]|{name,conclusion}]}'

# release page and assets
gh release view vX.Y.Z --json tagName,isDraft,isPrerelease,publishedAt,url,assets \
  --jq '{tag:.tagName,isDraft,isPrerelease,publishedAt,url,assets:[.assets[]|{name,size}]}'

# notes must include the expected groups and the Thanks block
gh release view vX.Y.Z --json body --jq .body

# the changelog committed on main
git show vX.Y.Z:CHANGELOG.md | sed -n '/^## vX.Y.Z /,/^## v/p'

# main CI for the changelog commit: a skipped matrix is normal (dedupe),
# see "Failure handling and known issues"
gh run list --branch main -w ci.yaml -L 3
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

## Failure handling and known issues

- **Tag already exists**: delete and recreate only when intentional
  (`git tag -d`, `git push origin :refs/tags/vX.Y.Z`), then dispatch
  `gh workflow run release.yaml -f version=vX.Y.Z`. The workflow replaces
  existing assets; the tag itself must still point at a green `main` commit.
- **`verify` failed**: the tag commit is not reachable from `origin/main`, or it
  has no green required checks. Move the tag to the merged commit (step 6) or
  make that commit green (re-run its main CI run, or land it through a PR),
  then dispatch again. Do not force a release past `verify`.
- **`dist-darwin` fails with a runner annotation**: v0.7.0 hit
  `The configuration 'macos-14-arm64-us-default' is not supported`
  (run `43880270913`) because the runner label had been retired; the fix was a
  workflow-only PR bumping the darwin `runs-on` (`macos-14` → `macos-15`,
  commit `24cc1c4`, PR #26), and the release recovered with
  `gh workflow run release.yaml -f version=v0.7.0` (run `43882320901`). Before
  blaming your change, compare the workflow at the last successful tag (the
  file that ran the dist jobs then — `ci.yaml` before the split, `release.yaml`
  now):

  ```bash
  git show <last-good-tag>:.github/workflows/ci.yaml | grep -n runs-on
  git show <last-good-tag>:.github/workflows/release.yaml | grep -n runs-on
  ```
- **Release has only source archives**: the `release` job did not receive
  packaged artifacts. Check the `dist/*.tar.gz` / `dist/*.zip` upload in the
  dist jobs and the `prebuilt/qrypt_*` globs in `.goreleaser.yaml`.
- **Release notes miss `### Thanks`**: confirm the tagged `CHANGELOG.md` has a
  `### Thanks` block (or `## Unreleased` with one), then run
  `scripts/release/add-release-thanks.sh vX.Y.Z`; it is idempotent. When there
  is no Thanks block at all, the script prints “跳过” and exits 0 — that is
  normal, not a failure.
- **`Test (windows)` fails on a docs-only PR**: the observed flake is
  `upload_task_test.go: task upload-... did not finish, last state=running`
  (Windows environment flake). Re-run once with
  `gh run rerun <run-id> --failed`; investigate only if it repeats.
- **Local `check-hygiene` aborts on ShellCheck**: the system shellcheck version
  differs from `ShellcheckVersion` in `tools.go` (`0.10.0`). Run
  `scripts/ci/install-tools.sh` and put `$QRYPT_TOOLS_DIR` (default
  `~/.cache/qrypt-tools`) first on `PATH`, or record explicitly why the gate was
  skipped.
- **`main` CI after the merge is skipped**: expected. The deduplicated strategy
  skips the check matrix (`check` and `test-windows`) when a push's tree is
  identical to an already-green PR head; the run itself still completes its
  `detect` job. It is not cancelled by later runs: `cancel-in-progress` only
  applies to pull requests, and `release.yaml` has its own concurrency group.
  Confirm the real state with `gh run list --branch main -w ci.yaml -L 3`.

## Safety

- Never print cookies, tokens, app passwords, signed URLs, or full
  `qrypt.toml` credentials in logs or PR bodies.
- Do not hand-edit version strings in code; the version comes from the tag via
  `-ldflags`.
- Do not upload release assets manually; let CI own the asset set.
