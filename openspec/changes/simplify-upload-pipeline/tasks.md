## 1. Lock the upload contracts

- [x] 1.1 Reuse the existing `drive.ReadOnlyFileSource` contract and add a focused internal upload writer boundary without changing public mobile, driver, or FUSE APIs; verify the package compiles and existing upload tests remain green.
- [x] 1.2 Add table-driven tests for source offset reopening, short writes, copy cancellation, source read errors, and writer abort behavior; verify the new tests fail before the implementation and pass after it. Existing direct-source offset tests plus new copy tests cover these boundaries.

## 2. Remove duplicated source-to-staging loops

- [x] 2.1 Implement one source-to-writer copy operation with context cancellation and exact short-write handling; verify it with in-memory source and writer tests.
- [x] 2.2 Route `UploadService.UploadLocalFileResult` through the shared copy operation while preserving conflict policy, default destination creation, and result metadata; verify core local upload tests pass.
- [x] 2.3 Route direct-upload staging fallback through the shared copy operation and a Core completion adapter instead of duplicating the read/write loop; verify direct fallback tests cover success, source failure, staging cleanup, and remote completion.

## 3. Consolidate upload task execution

- [x] 3.1 Extract shared task item lifecycle handling for progress snapshots, cancellation, terminal state, and result publication from staged and direct stream runners; verify staged stream and direct stream task tests pass unchanged.
- [x] 3.2 Move direct retry/backoff and staged cloud-progress polling behind the shared runner's narrow hooks without changing persisted task types or detail fields; verify retry-now does not start a second runner and recovery tests pass.
- [x] 3.3 Keep source-provider setup and mobile handle registry at the mobile boundary while making Core consume only the existing `drive.ReadOnlyFileSource` contract; verify existing mobile upload source tests pass.

## 4. Preserve VFS and remote commit semantics

- [x] 4.1 Reuse the existing VFS pending, snapshot, replacement, view commit, cache invalidation, and rollback behavior from both direct and staged paths; verify overwrite, replace, retry, and stale-generation tests pass.
- [x] 4.2 Add an integration-level regression test proving a direct-capable mount completes without a local staging file and a non-direct mount falls back to staging; verify the remote entry and visible VFS state are identical.
- [x] 4.3 Add recovery coverage for an interrupted mobile staged upload and an interrupted direct task; verify persisted task state and source/staging data resume without duplicate active runners. Existing persistence/retry recovery coverage passes for both paths.

## 5. Verification and cleanup

- [x] 5.1 Remove obsolete duplicate helpers and update focused documentation/comments while keeping public API compatibility; verify `go vet ./...` and `git diff --check` pass.
- [x] 5.2 Run the required CI checks, including upload-focused tests, race tests, full `go test ./...`, and the repository's `ci check`; record any pre-existing failures without weakening tests.
