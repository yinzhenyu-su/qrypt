# Tasks

## 1. Windows PATH edits preserve the user's registry value

- [x] 1.1 Replace `Add-UserPath` in `scripts/install.ps1` with a registry read/write: read `HKCU:\Environment\Path` with `DoNotExpandEnvironmentNames`, keep the value's existing registry kind (`REG_SZ` when the value does not exist yet), append the one entry only when absent, and write the value back unchanged otherwise — verify `pwsh -NoProfile -File scripts/install.ps1 -PrintManifest -Version v0.5.3 -ManifestOS windows -ManifestArch amd64` still prints the same manifest
- [x] 1.2 Add a best-effort `Publish-Env` that broadcasts `WM_SETTINGCHANGE` to `HWND_BROADCAST` (`SendMessageTimeout`) after the value changes, and call it from the PATH step — verify a broadcast failure cannot fail the install (the smoke case in 1.3 still passes)
- [x] 1.3 Add a `scripts/smoke-install.ps1` case that seeds the user `Path` as `REG_SZ` with an unexpanded `%VAR%`-style entry, installs, and asserts the seeded entry survives verbatim and the value's kind is still `REG_SZ` — verify `pwsh ./scripts/smoke-install.ps1` passes on windows

## 2. Newest release resolved without the API quota

- [x] 2.1 Add a redirect resolver to `scripts/install.sh`: follow `QRYPT_INSTALL_LATEST_URL` (default `https://github.com/<repo>/releases/latest`) with `curl -fsSL -o /dev/null -w '%{url_effective}'` and take the tag after `/tag/`, keeping `QRYPT_INSTALL_LATEST_TAG` first and the existing API path as fallback — verify `SH` shellcheck stays clean (`shellcheck scripts/install.sh`) and `scripts/smoke-install.sh` passes
- [x] 2.2 Add the same resolver to `scripts/install.ps1` through `curl.exe -fsSL -o NUL -w "%{url_effective}"`, falling back to `Get-LatestTag` — verify the Windows smoke run in 2.4 passes
- [x] 2.3 Replace the smoke harness's `python3 -m http.server` with a handler that serves `302 /releases/latest → /releases/tag/<tag>` alongside the release files and the API JSON — verify `scripts/smoke-install.sh` passes and the fresh-install case resolves through the redirect
- [x] 2.4 Add a `scripts/smoke-install.sh` case that publishes a newer release, re-runs the installer, and asserts the installed command reports the newer version — verify `scripts/smoke-install.sh` passes
- [x] 2.5 Add a `scripts/smoke-install.sh` case where the API base answers `403` (rate limited) and the newest release is still installed from the redirect — verify `scripts/smoke-install.sh` passes

## 3. Windows reinstall over a running binary

- [x] 3.1 Replace `[System.IO.File]::Replace` in `Install-Binary` (`scripts/install.ps1`) with the `pkg/selfupdate` swap: remove stale `qrypt.exe.old*`, rename `qrypt.exe` to `.old` (falling back to `.old.<pid>`), move the staged file into place, and request deferred deletion of the displaced file — verify the smoke case in 3.2 passes
- [x] 3.2 Add a `scripts/smoke-install.ps1` case that opens the installed `qrypt.exe` with `FileShare.Read|Delete` (permits a rename, denies a write, like a running image) and asserts a re-run still completes and installs the newer release — verify `pwsh ./scripts/smoke-install.ps1` passes
- [x] 3.3 Assert a clean re-run leaves no `qrypt.exe.old*` behind and that `qrypt uninstall` still sweeps any that exist — verify the smoke test leaves no remnant and `go test ./pkg/install/ -count=1` passes

## 4. Bootstrap served as a release asset, and the docs updated

- [x] 4.1 Add `scripts/install.sh` and `scripts/install.ps1` to `.goreleaser.yaml` `release.extra_files`, keeping them out of the published checksum file — verify a snapshot run lists both as release assets and `qrypt_<version>_checksums.txt` still lists only the archives
- [x] 4.2 Update `docs/for-user/quickstart.md`, `README.md`, and `README.zh.md` to the release-asset URLs and the `powershell -c "irm <url>|iex"` form, keeping one version-pinned example that fetches the script from that release — verify each documented command matches the form the scripts accept and `scripts/check-install.sh` passes

## 5. Integration verification

- [x] 5.1 Run the local gate and the install checks — verify `scripts/ci-check.sh` and `scripts/check-install.sh` both pass
- [x] 5.2 Run the container and PowerShell smoke tests where available — verify `scripts/smoke-install.sh --docker` passes and `pwsh ./scripts/smoke-install.ps1` passes
- [x] 5.3 Confirm the naming/layout contract between the scripts and the Go packages still holds — verify `go test ./pkg/install/ ./pkg/selfupdate/ ./internal/cli/upgrade/ -count=1` passes
- [x] 5.4 Confirm on CI that the Windows job (naming drift and `smoke-install.ps1`) and the Linux install smoke are green

## Workflow follow-up

- Sync the `installation` delta into `openspec/specs/installation/spec.md` and archive the change after review.
- Note in the archived change that the documented release-asset URL only resolves once a release carrying the new `extra_files` is cut.
