## 1. Layout and shared contract

- [x] 1.1 Add the install-layout constants (real binary dir `~/.qrypt/bin`, symlink dir `~/.local/bin`, binary name, rc marker) in one place consumed by the Go side; verify with a unit test pinning the paths and marker strings
- [x] 1.2 Add a check that fails when the asset/checksum/inner-binary naming in `scripts/install.sh` and `pkg/selfupdate` disagree; verify it passes when they match and fails when one is edited
- [x] 1.3 Wire that check into the repo CI entry point; verify `scripts/ci-check.sh` runs it and fails on an injected mismatch

## 2. Bootstrap script (`scripts/install.sh`)

- [x] 2.1 Implement platform detection (OS + arch) and asset-name derivation matching the updater; verify each supported pair and the unsupported-platform error
- [x] 2.2 Implement download of the newest release, plus `--version <v>` to pin one; verify both against a local fixture server and the "release not found" path
- [x] 2.3 Implement sha256 verification against the release checksum file before extraction; verify a mismatch and a missing entry both abort without touching an existing install
- [x] 2.4 Implement installing the real binary to `~/.qrypt/bin/qrypt` (executable, versionless) and creating the `~/.local/bin/qrypt` symlink; verify the layout and that a re-run converges
- [x] 2.5 Refuse to overwrite a pre-existing unmanaged `~/.local/bin/qrypt` unless consent is given; verify the refusal leaves the file unchanged
- [x] 2.6 After install, detect a `qrypt` on `PATH` that is not the managed copy and warn; verify with a shadowing copy present

## 3. Shell PATH configuration

- [x] 3.1 Skip all editing when `~/.local/bin` is already on `PATH`; verify no rc file is modified
- [x] 3.2 Append a single marker-delimited block to a login-shell startup file (zsh/bash/fish) when the dir is missing; verify the block is present and that a second run adds nothing
- [x] 3.3 Fall back to printing the lines when no candidate file is writable or the shell is unknown; verify the script makes no edit and prints a runnable line
- [x] 3.4 Print the activation step for the current session (`source <file>` / `exec $SHELL`); verify the message names the edited file

## 4. Uninstall command

- [x] 4.1 Add `internal/cli/uninstall` with the summary, long description, and examples required by the `cli-help` contract, and register it in `internal/cli/command_root.go`; verify its help renders and the flags follow the ladder
- [x] 4.2 Implement removal of the managed symlink only when it resolves to the managed binary; verify an unrelated symlink and a non-symlink file are left in place
- [x] 4.3 Implement removal of the managed binary and of the marker-delimited `PATH` block; verify a pre-existing user `PATH` line is untouched
- [x] 4.4 Preserve user data by default and remove it only with the explicit removal flag; verify both paths
- [x] 4.5 Require confirmation unless told to proceed non-interactively, and make the command idempotent; verify a second run reports nothing to remove and exits successfully
- [x] 4.6 Map exit codes consistently with the rest of the CLI; verify with exit-code tests

## 5. Docs, CI verification, and gates

- [x] 5.1 Rewrite the install section of `docs/for-user/quickstart.md` around the bootstrap command, keeping the manual steps as a fallback; verify the documented commands match the script
- [x] 5.2 Document `qrypt uninstall` in `docs/for-user/cli.md`; verify the documented behavior matches the command
- [x] 5.3 Add shellcheck plus a container smoke test (install, then `qrypt version`) to CI; verify both pass and the smoke test fails on a broken script
- [x] 5.4 Run `scripts/ci-check.sh` and the layered test scripts; verify they pass
- [x] 5.5 Extend install documentation to `README.md`, `README.zh.md`, and the developer quality-gates page; verify the quick-start command matches `scripts/install.sh`

## 6. Windows install (PowerShell)

- [x] 6.1 Add `scripts/install.ps1` with `-Version`, `-Force`, and a `-PrintManifest` debugging switch, plus platform and architecture detection matching the updater; verify `-PrintManifest` for `windows/amd64` and `windows/arm64`
- [x] 6.2 Implement download and `Get-FileHash -Algorithm SHA256` verification of the `.zip` against the release checksum before `Expand-Archive`; verify a mismatch aborts without installing and a failed download leaves a prior install intact
- [x] 6.3 Install the binary to `%USERPROFILE%\.qrypt\bin\qrypt.exe` (versionless) and add that directory to the **user-scope** `PATH` idempotently; verify repeated runs add no duplicate entry and need no administrator rights
- [x] 6.4 Refuse to overwrite an unmanaged `qrypt.exe` without `-Force`, and after install warn when `Get-Command qrypt` resolves to a different copy; verify both
- [x] 6.5 Add a Windows code path to `pkg/install` (build-tagged) for the user-registry `PATH` entry, with no symlink or rc files; verify with Windows-tagged unit tests
- [x] 6.6 Make `qrypt uninstall` remove the Windows binary and its user-scope `PATH` entry on windows; verify with a Windows-tagged test that an unrelated `PATH` entry is untouched and a second run is a no-op
- [x] 6.7 Extend the naming drift check to also run `install.ps1 -PrintManifest` (run on the Windows CI job; skip where `pwsh` is absent); verify it fails on an injected mismatch
- [x] 6.8 Add a Windows install smoke test to the `test-windows` CI job that installs from a local fixture and runs `qrypt version`; verify it passes and fails on a broken script
- [x] 6.9 Document the Windows one-liner in `quickstart.md`, `README.md`, and `README.zh.md`, and note the Windows behavior in `docs/for-user/cli.md`; verify the documented command matches `install.ps1`

## 7. Windows uninstall remnants

- [x] 7.1 Sweep displaced `qrypt.exe.old*` files beside the managed binary in the Windows uninstall path and remove the install directory only once it is empty; verify with a Windows-tagged test that a leftover `.old` is removed and the now-empty directory is removed
- [x] 7.2 Extend `install.UninstallReport` with the remnant that could not be removed (the running image) and print it from `qrypt uninstall`; verify with a Windows-tagged test that the report and the command output name the deferred file instead of claiming a clean removal
- [x] 7.3 Keep the user-facing uninstall docs minimal — the command plus the data-preserved default — and leave the Windows remnant behavior to the command's own output; verify the docs still match the command
- [x] 7.4 Invoke `pwsh` in the PowerShell naming-drift test with `-ExecutionPolicy Bypass`; verify `go test ./pkg/install -run TestPowerShellInstallerNamingMatchesUpdater` passes where the host policy blocks unsigned or remote scripts
- [x] 7.5 Run the Windows-tagged `pkg/install` tests on Windows and the layered test scripts on this host; verify they pass (see `scripts/ci-check.sh` and the `test-windows` CI job)
