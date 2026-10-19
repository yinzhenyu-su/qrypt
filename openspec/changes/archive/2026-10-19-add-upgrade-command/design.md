## Context

See `proposal.md` for motivation and `specs/self-update/spec.md` for requirements.

Constraints discovered in the codebase:

- Releases are published by GoReleaser with prebuilt archives. Asset names are
  `qrypt_<version>_<goos>_<goarch>.tar.gz` (linux, darwin) or `.zip` (windows), where `<version>`
  has no leading `v`, plus `qrypt_<version>_checksums.txt` with sha256 lines over those asset names.
- The archive's inner file is **not** `qrypt`: it is `qrypt-linux-amd64`, `qrypt-darwin-arm64`, and
  `qrypt-windows-amd64.exe`. Installation must rename it.
- The release repository is `yinzhenyu-su/qrypt`, which differs from the Go module path
  `github.com/yinzhenyu/qrypt`, so the update source cannot be derived from build metadata.
- `pkg/util/fileutil_atomic.go` already provides same-directory temp-write plus
  `replaceLocalFile` (Unix `os.Rename`; Windows `MoveFileEx(REPLACE_EXISTING)`), and the CLI has no
  privilege-escalation precedent.
- `pkg/buildinfo.Current()` reports `Version`, `Commit`, and `Dirty`; local builds resolve to
  `git describe` values such as `v0.5.3-10-g9d16c54`, which are not release versions.
- Every release so far is stable (no prerelease or draft), so no prerelease channel exists to design
  around.

## Goals / Non-Goals

**Goals:**

- A single `qrypt upgrade` path that is safe by default: verified, atomic, and non-destructive on any
  failure.
- Keep the network/download/verify/swap machinery testable without network access.
- Reuse the existing atomic-replacement primitives rather than introducing a parallel mechanism.

**Non-Goals:**

- Passive update notifications or any network call on commands other than `upgrade`.
- Signature verification, delta/patch updates, or package-manager integration.
- Self-escalation to root.

## Decisions

### Split: `pkg/selfupdate` (behavior) + `internal/cli/upgrade` (surface)

Put all release lookup, version comparison, download, verification, extraction, and replacement in
`pkg/selfupdate`, and keep the cobra command thin (flag parsing, output, exit-code mapping). This
follows the repo's package-boundary convention that `internal/cli` command packages do not carry
transfer logic, and makes the risky logic unit-testable against an `httptest` server.

_Alternative considered:_ implement everything inside `internal/cli/upgrade`. Rejected — the swap and
verification logic would only be reachable through a full command invocation.

### Update source: GitHub Releases REST API behind a small interface

Use `GET /repos/<owner>/<repo>/releases` (and the `latest` form) over `net/http`, selecting the newest
non-prerelease, non-draft release. Expose the base URL as a package variable so tests point it at
`httptest`. Honor `GITHUB_TOKEN`/`GH_TOKEN` when present to raise the unauthenticated 60 req/hr limit.

_Alternative considered:_ scrape the releases HTML page. Rejected — brittle and unauthenticated in the
same way, without a stable schema.

### Repository identity as a constant, not derived from the module path

Because `go.mod` (`yinzhenyu/qrypt`) disagrees with the release repo (`yinzhenyu-su/qrypt`), the
command uses a single `const` (or link-time variable) for `owner/repo`. Tests override it; forks can
reinterpret it.

### Version gate and comparison

Reuse a minimal semver comparison rather than pulling a dependency. Treat the current version as
upgradeable only when it parses as a plain `vMAJOR.MINOR.PATCH` release; otherwise refuse unless
`--force`. This is why `git describe`-style strings are rejected: they carry no release to compare to.

_Alternative considered:_ add a semver library. Rejected — one small comparison does not justify a
dependency, and the version space here is strictly `vX.Y.Z`.

### Verification is mandatory, not optional

Always fetch the release's checksum file and compare the downloaded archive's sha256; there is no
"skip verification" flag. This is the only integrity control available today (checksums are published
in the same release), which the spec and proposal acknowledge as a non-goal to strengthen now.

### Atomic replacement, per platform

Reuse `pkg/util`'s atomic write and `replaceLocalFile`, extending it with a Windows swap that moves
the running `.exe` aside before placing the new binary, since a running image cannot be replaced in
place. Cleanup of the displaced `.old` file is best-effort at the start of the next upgrade (and a
reboot-delete fallback), so a leftover file never blocks a later run. On Unix the replacement targets
the symlink-resolved real path so symlinked installs keep working.

### Windows replacement is verified on Windows, without a production update seam

The Windows swap has a branch no Unix run reaches: the target is the executable the process is
running, so the rename-aside fallback and the leftover cleanup must be exercised on Windows. The
package is kept portable and added to the `test-windows` CI unit list; the parts of the suite that
assert Unix-only facts (an executable permission bit, a directory made read-only with `chmod`) are
guarded so they no longer fail on Windows; and the running-image branch is covered by two tests: a
locked-file swap (hold the target with a handle that denies delete, then swap) and a helper-process
test in which a child replaces its own executable, following the repo's existing
`exec.Command(os.Args[0], "-test.run=…")` pattern. No test-only switch is added to the released
client, so a shipped binary's update source cannot be redirected through the environment.

_Alternatives:_ a PowerShell end-to-end smoke of `qrypt.exe upgrade` against a local release server —
rejected because it would require a runtime base-URL override in the client, while the in-process
tests already drive the same `Run → install → swap` path; asserting executable permissions on
Windows — impossible, since the platform has no such mode bit.

### Privilege handling

Detect writability of the resolved path and refuse with a copy-pastable re-run command; never invoke
`sudo` or a prompt. This matches the CLI's existing "fail with a runnable next step" convention and
keeps credential-handling binaries from self-elevating.

### Exit-code mapping

Reuse the CLI's existing mapping: success and "already up to date" → 0, usage errors (unparsable
`--to`) → 2, runtime failures (network, integrity, permission) → 1. This keeps `upgrade` consistent
with the documented exit-code table.

## Risks / Trade-offs

- [Checksums share an origin with the payload, so they do not defend against a compromised release]
  → Accept for this change; note signature verification as a deliberate non-goal to revisit.
- [GitHub API rate limits can reject lookups] → Honor `GITHUB_TOKEN`/`GH_TOKEN` and surface a clear
  error naming the limit.
- [Windows rename-swap can leave a `.old` file] → Best-effort cleanup at the next upgrade plus a
  reboot-delete fallback; a leftover file must never fail a later upgrade.
- [Replacing a binary that a package manager owns would silently desync the install] → The non-writable
  refusal catches read-only locations; a fully general package-manager detection is out of scope.
- [A network-touching command increases the trust surface of the binary] → No self-escalation, mandatory
  verification, and no writes before verification succeeds.

## Migration Plan

Additive: a new command and a new package, no existing behavior changes. Shipping requires no data
migration. Rollback is deleting the command registration and package; no persisted state is written.

## Open Questions

- Whether a future release should publish a detached signature (for example cosign) and whether the
  updater should then require it.
- Whether a prerelease/nightly channel will exist and what selection rule it should follow.
