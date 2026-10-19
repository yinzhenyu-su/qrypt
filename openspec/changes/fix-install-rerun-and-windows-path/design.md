# Design

## Context

See `proposal.md` — Why for motivation. Constraints that shape the approach:

- `scripts/install.sh` (linux/darwin) and `scripts/install.ps1` (windows) both resolve the newest tag
  with `GET https://api.github.com/repos/<repo>/releases/latest` (`latest_tag` / `Get-LatestTag`),
  which is limited to 60 requests/hour per unauthenticated IP. Both scripts already re-download and
  reinstall on every run, so a re-run reaches the newest release whenever resolution succeeds; the
  failure modes are resolution and the Windows swap.
- `scripts/install.ps1` edits the user `PATH` with `[Environment]::SetEnvironmentVariable('Path', …,
  'User')`, which expands `%VAR%` references on read and writes `REG_SZ`. `pkg/install/userpath_windows.go`
  (used by `qrypt uninstall`) reads the raw value with the registry API and preserves `EXPAND_SZ`, so
  the installer and the uninstaller disagree after one round trip.
- `scripts/install.ps1` swaps the binary with `[System.IO.File]::Replace`, which fails while
  `qrypt.exe` is running. `pkg/selfupdate/install_windows.go` already solves this: rename the running
  image to `<binary>.old` (falling back to `.old.<pid>`), place the new file, then ask Windows to
  delete the displaced one. `pkg/install/uninstall_windows.go` already sweeps `qrypt.exe.old*`.
- Release assets are assembled by goreleaser from `prebuilt/` through `release.extra_files`, and the
  release job checks out the tagged tree, so the bootstrap scripts can be attached there.
- The naming/layout contract between the scripts and `pkg/selfupdate`/`pkg/install` is enforced by
  `pkg/install/naming_drift_test.go`.

## Goals / Non-Goals

**Goals:**

- A re-run reaches the newest release without a token and without depending on the rate-limited API.
- A re-run succeeds while the installed binary is running, on every platform.
- The Windows `PATH` edit is indistinguishable from `pkg/install`'s: same text, same registry type.
- The documented commands are one paste per platform, and the script comes from the release it installs.

**Non-Goals:**

- Registering an uninstall entry under `HKCU\…\Uninstall` (bun does this) — it targets Windows Settings,
  not the behavior this change is about.
- Making `curl.exe` the general downloader with an `Invoke-RestMethod` fallback, download progress
  bars, or a minimum-Windows-version gate.
- Dispatching from `scripts/install.sh` to the PowerShell script when it runs under MSYS/Git Bash.

## Decisions

### The bootstrap is served from the release, not from `main`

Publish `scripts/install.sh` and `scripts/install.ps1` as release assets (add them to
`.goreleaser.yaml` `release.extra_files`) and document
`https://github.com/yinzhenyu-su/qrypt/releases/latest/download/install.{sh,ps1}`. This resolves the
archived change's open question toward the release-pinned side: the script and the binaries it
installs ship together, and no later `main` commit can change what an already-pasted command does.
`releases/latest/download` is served by GitHub's release CDN — no API call, no rate limit. The scripts
are not added to the published checksum file; they are not archives, and the integrity contract covers
the archive.

_Alternatives:_ keep `raw.githubusercontent.com/<repo>/main/scripts/install.sh`, which moves under the
user; a GitHub Pages `CNAME` on an owned domain, which can be layered on later without touching the
scripts.

### The Windows command is `powershell -c "irm <url>|iex"`

Document exactly that form. It is pasteable into `cmd.exe`, Git Bash, and any other shell, not only an
already-open PowerShell session, and it mirrors bun's documented form. The version-pinned variant
fetches the script from that release, `…/releases/download/v0.5.0/install.ps1`.

### Newest tag is resolved from the `latest` redirect, with the API as fallback

Resolve by following `https://github.com/<repo>/releases/latest` to its final URL and taking the tag
from `/tag/<tag>`:

- sh: `curl -fsSL -o /dev/null -w '%{url_effective}' "$LATEST_URL"`, then strip up to `/tag/`.
- ps1: the same through `curl.exe -fsSL -o NUL -w "%{url_effective}" "$LATEST_URL"`.

That is one request to the website host, not the API, so it is outside the anonymous API quota. On any
failure (no curl, blocked host) fall back to the existing API path, which keeps token users and the
smoke harness working. `QRYPT_INSTALL_LATEST_TAG` still short-circuits both, and a new
`QRYPT_INSTALL_LATEST_URL` override lets a test point the redirect at a local server.

_Alternatives:_ the static `releases/latest/download/<asset>` path bun uses — rejected because qrypt's
asset names embed the version, so the version is needed before the download URL can be built. Keeping
API-first — leaves the rate-limit failure in place.

### Windows PATH is edited through the registry, preserving type and text

`install.ps1` gains `Read-Env`/`Write-Env` equivalents of `pkg/install/userpath_windows.go`: read
`HKCU:\Environment\Path` with `DoNotExpandEnvironmentNames`, keep the value's existing registry kind
(`REG_SZ` when the value does not exist yet), write it back unchanged apart from the appended entry,
and broadcast `WM_SETTINGCHANGE` to `HWND_BROADCAST` so newly started processes see the new value.
The comparison logic stays as it is (case-insensitive, tolerant of a trailing separator, appends one
entry at most).

_Alternatives:_ keep `[Environment]::SetEnvironmentVariable` — leaves `%USERPROFILE%`-style entries
expanded and the kind rewritten, so `qrypt uninstall` sees a different value than the installer wrote.
bun's variant forces `REG_EXPAND_SZ` whenever the value contains `%`; rejected because converting a
`REG_SZ` value would start expanding entries the installer did not add, which the `installation` spec
forbids. `setx` — truncates long values and is already rejected.

### Windows reinstall displaces a running binary instead of failing

Replace `[System.IO.File]::Replace` with the `pkg/selfupdate` dance, reusing its file names so
`qrypt uninstall`'s existing `qrypt.exe.old*` sweep already clears the leftovers: remove any stale
`.old*` beside the binary, rename `qrypt.exe` to `qrypt.exe.old` (fall back to `.old.<pid>` when the
plain name is held), move the staged file into place, then `MoveFileEx(…, MOVEFILE_DELAY_UNTIL_REBOOT)`
for the displaced file. On linux and darwin the existing staged copy plus `mv -f` already replaces a
running image by rename, so only windows changes.

_Alternatives:_ detect the process and tell the user to close it (bun) — leaves "re-run while running"
unsupported, which is the case the requirement targets. A detached helper process — disproportionate
for one file.

### Tests

- `scripts/smoke-install.sh`: replace `python3 -m http.server` with a small handler that can serve a
  `302` from `/releases/latest` to `/releases/tag/<tag>`, so the redirect resolver is exercised, and
  add a case that publishes a **newer** release and asserts a re-run installs it.
- `scripts/smoke-install.ps1`: add a case that opens the installed `qrypt.exe` with
  `FileShare.Read|Delete` — which, like a running image, permits a rename but denies a write — and
  asserts a re-run still completes and installs the newer release.
- `pkg/install`: the naming-drift test remains the scripts/new-repo contract; extend the manifest only
  if the resolver introduces a value the scripts must share.

## Risks / Trade-offs

- [Reading the tag from a redirect breaks if GitHub changes the URL shape] → the parse anchors on
  `/tag/`, and every failure falls back to the API path.
- [The Windows PATH rule now exists in PowerShell and in Go] → keep both to the same rule and cover the
  PowerShell side with the smoke test's PATH assertions; a single shared implementation is not possible
  across the two languages.
- [The installer can leave a `.old` remnant] → it is already covered by uninstall's sweep, and the
  installer removes stale ones before swapping.
- [Release assets are immutable per tag] → fixing a bootstrap script requires cutting a release.
  Acceptable: the scripts are small, and the version pin is the point.
- [`curl.exe` may be absent on old Windows] → the resolver falls back to the API path, which is what
  ships today.

## Migration Plan

Additive and release-scoped. The docs switch to the release-asset URL, which exists only once a release
carrying the new `extra_files` is cut; until then the `main` URL still works, so both may be documented
briefly. Rollback is reverting the docs and dropping the two `extra_files` entries; nothing on a user's
machine depends on the change beyond the install layout, which does not change.
