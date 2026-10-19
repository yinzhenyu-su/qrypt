## 1. Release lookup and version model

- [x] 1.1 Create `pkg/selfupdate` with a release model (tag, prerelease/draft flags, asset names and download URLs) and a GitHub Releases client over an injectable base URL; verify with an `httptest` server returning a fixture release list
- [x] 1.2 Implement "newest applicable release" selection that skips prereleases and drafts; verify with a fixture mixing stable and prerelease entries
- [x] 1.3 Implement asset selection for a `(goos, goarch)` pair over the `qrypt_<version>_<goos>_<goarch>` naming; verify success plus the "no asset for platform" error case
- [x] 1.4 Implement release-version parsing and semver comparison, including the plain `vX.Y.Z` release gate that rejects `git describe`-style builds; verify with table-driven tests
- [x] 1.5 Implement a repository-identity constant (owner/repo) independent of the module path, overridable in tests; verify the client uses the overridden value

## 2. Download and integrity verification

- [x] 2.1 Implement streaming download of a selected asset to a temp file with context timeout and clear network errors; verify with an `httptest` server, including a server that closes mid-body
- [x] 2.2 Implement checksum retrieval and sha256 comparison against the release's `*_checksums.txt`; verify match proceeds and mismatch aborts
- [x] 2.3 Cover missing-checksum-entry handling as a distinct error; verify the error names the asset and no install step runs

## 3. Extraction and atomic replacement

- [x] 3.1 Implement extracting the inner binary from `.tar.gz` and `.zip` by known inner name; verify both formats with committed fixture archives
- [x] 3.2 Implement Unix replacement of the symlink-resolved real path using `pkg/util` atomic primitives, preserving executable mode; verify a swap test and a "symlink still resolves to new binary" test
- [x] 3.3 Extend `pkg/util` (or `pkg/selfupdate`) with the Windows rename-and-place swap and best-effort cleanup of a leftover `.old` file; verify with a Windows-tagged unit test that a second swap succeeds with a leftover present
- [x] 3.4 Ensure every failure path (download, verify, extract) leaves the current executable unchanged; verify with an injected failure at each stage
- [x] 3.5 Implement writability detection of the resolved path and return a non-escalating error carrying the path and a re-run command; verify the message contains a runnable command and no privilege prompt occurs

## 4. CLI command surface

- [x] 4.1 Add `internal/cli/upgrade` with the command summary, long description, and examples required by the `cli-help` contract; verify `qrypt upgrade --help` renders all three
- [x] 4.2 Wire flags `--check`, `--to`, `--force`, `--json` (marking `--to`/`--force` advanced) and register the command in `command_root.go`; verify the flags appear per the help ladder, including under `--help --advanced`
- [x] 4.3 Implement the `--check` path that reports current vs available and performs no download or write; verify with a fixture and an assertion that no write occurs
- [x] 4.4 Implement human progress output naming each step and the `--json` single-object result; verify JSON output is parseable and carries current version, target version, and outcome
- [x] 4.5 Implement exit-code mapping (0 success/already-up-to-date, 1 runtime, 2 usage) including an unparsable `--to`; verify with exit-code tests

## 5. Integration, docs, and gates

- [x] 5.1 End-to-end test: serve a fake newer release over `httptest`, run the upgrade against a temp copy of a binary, and verify the file is replaced and reports both versions
- [x] 5.2 Document `qrypt upgrade` in `docs/for-user/cli.md`; verify the documented behavior matches the command
- [x] 5.3 Run `scripts/ci-check.sh` and the layered test scripts; verify they pass

## 6. Windows verification

- [x] 6.1 Guard the Unix-only assertions in `pkg/selfupdate` — the read-only-directory writability test and the executable-bit checks in the install and tar-gz extraction tests — so the package passes on Windows; verify by running the `GOOS=windows` test binary on Windows and seeing no failures
- [x] 6.2 Add `./pkg/selfupdate` and `./internal/cli/upgrade` to the `test-windows` unit-test list in `.github/workflows/ci.yaml`; verify the job runs both packages
- [x] 6.3 Add a Windows-tagged swap test that holds the target with a handle denying delete, then calls `swapIntoPlace`; verify it asserts the `.old.<pid>` fallback and that the new binary lands at the target path
- [x] 6.4 Add a Windows-tagged helper-process test in which a child replaces its own running executable, following the repo's `exec.Command(os.Args[0], "-test.run=…")` pattern; verify the running image is moved aside and the replacement is in place
- [x] 6.5 Run the Windows-tagged `pkg/selfupdate` tests on Windows and the layered test scripts on this host; verify they pass
