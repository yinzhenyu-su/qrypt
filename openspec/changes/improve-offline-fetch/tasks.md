## 1. Provider capture and verification

- [ ] 1.1 Capture a real Quark parse response and record the meaning of `path`, `file_dirs`, and `torrent_name`; verify the recorded sample states whether `path` is torrent-relative or cloud-absolute
- [ ] 1.2 Capture a real Quark submit response and record what `fid` and `dir_fid` name; verify the sample states whether `fid` is the resource root and whether a single-file source gets a folder
- [ ] 1.3 Capture a completed Quark `/task` observation for a multi-file torrent and record whether `offdl_result.list` omits any staged file; verify the sample shows the reported entries against the parsed list
- [ ] 1.4 Capture `/file/sort` for a staging directory and record whether entries expose `pdir_fid` and whether subdirectories can be listed; verify the sample confirms the walk-down from the output root is feasible
- [ ] 1.5 Reconcile the captures with design D1/D3 and record any deviation; verify `design.md` is updated if the approach must change, otherwise record "no change"

## 2. Provider-neutral contract

- [x] 2.1 Add `Path` to `drive.OfflineFile` and to the Core result-file model with JSON tags; verify a round-trip test decodes both an old snapshot without `Path` and a new one with it
- [x] 2.2 Add the availability signal to `drive.OfflineDownload`; verify a unit test maps it and that a provider omitting it still decodes
- [x] 2.3 Add the `offline_path_invalid` error code and its retryability; verify the error-code test lists it as non-retryable
- [x] 2.4 Persist the staged relative path and the task output root in offline detail; verify a persist/decode test keeps both across a restart

## 3. Quark provider

- [x] 3.1 Resolve staged relative paths by walking down from the output root via parent-directory identity; verify a driver test with a nested fixture returns the expected relative paths
- [x] 3.2 Report the availability signal from `download_task_finish` and terminal state; verify a driver test asserts it for instant-available and non-instant responses
- [x] 3.3 Discover files the result list omitted, scoped to the task's output root; verify a driver test with a thin result list returns the extra entries

## 4. Core fetch layout

- [x] 4.1 Gate the transfer on availability or a terminal observation, replacing the create-time short-circuit; verify a runner test starts a pre-terminal transfer only when availability is reported
- [x] 4.2 Resolve each target at `destination + staged path` with a file-name fallback, and fail a single-file destination with a directory layout with `offline_path_invalid`; verify fetch tests for multi-file, nested, fallback, and rejection
- [x] 4.3 Create each parent directory recursively for local and namespace destinations; verify a test writes a nested namespace destination
- [x] 4.4 Update staging-path resolution, destination reconciliation, and item tracking to use the staged path; verify reconcile and item tests cover a nested layout
- [x] 4.5 Keep cleanup removing only the staged fids it created; verify the existing cleanup tests still pass and a staged path does not change which fids are removed

## 5. Path safety

- [x] 5.1 Implement the per-component validator; verify a table test over absolute paths, `..`, empty components, control characters, Windows-reserved names, and trailing dots or spaces
- [x] 5.2 Fail the task with `offline_path_invalid` and write nothing on rejection; verify a task-level test asserts the code and an empty destination
- [x] 5.3 Refuse a destination path that resolves through an existing symlinked ancestor leaving the destination root; verify a local-destination symlink test
- [x] 5.4 Fail on two results resolving to the same destination; verify a collision test that neither file is truncated
- [x] 5.5 Apply the validator to flat placement too; verify a test that a file name containing `..` no longer escapes the destination

## 6. CLI default and progress

- [x] 6.1 Make the default non-blocking (submit, wait for the first observation, print the snapshot and an observe hint) and add `--wait`; verify CLI tests for the default and `--wait`
- [x] 6.2 Keep `--no-wait` as a deprecated no-op; verify the flag is accepted and behaves like the default
- [x] 6.3 Share one progress renderer between `fetch --wait` and `offline wait`, writing to stderr; verify a renderer unit test and that `--json` stdout still holds exactly one document
- [x] 6.4 Add `--flat`; verify a CLI test that it forces name-only placement

## 7. Docs, compatibility, integration

- [x] 7.1 Update `docs/for-user/cli.md` for the default, `--wait`, the mirrored layout, and `--flat`; verify the documented examples match the implemented flags
- [x] 7.2 Update the `docs/for-developer/quality-gates.md` offline smoke steps; verify the described flow matches the new default and `--wait`
- [x] 7.3 Add a compatibility test that an old offline snapshot without a staged path still renders and resumes; verify it passes
- [x] 7.4 Run the project CI check and the layered test scripts; verify they pass

## 8. Resource folder cleanup

- [x] 8.1 Extend fetch cleanup to also remove the resource folder the task created once every file under it is one this cleanup removed; verify tests for a multi-file folder (removed), nested folders (removed), an unrelated entry (folder kept), files staged directly in the parse destination (parse destination untouched), and `--keep-cloud` (folder kept)
- [x] 8.2 Run the project CI check and the layered test scripts; verify they pass
