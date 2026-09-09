## 1. Baseline and safe cleanup

- [x] 1.1 Confirm current upload call graph and run focused core/mobile/vfs upload tests before edits.
- [x] 1.2 Route local upload task and direct fallback completion through one Core completion entry without changing polling behavior or task result fields; verify upload task and direct stream tests.
- [x] 1.3 Move `uploadCopyChunkSize` to the copy module and remove obsolete imports/comments; verify `gofmt`, `go vet`, and copy tests.

## 2. Stream wrapper decision

- [x] 2.1 Inspect all stream wrapper callers and decide whether a stream session can preserve closed, cancel, retry, and recovery semantics; record the decision in the implementation diff.
- [x] 2.2 If safe, replace redundant Core stream forwarding with the session boundary; otherwise retain the wrappers and document why they remain. Verify staged stream, mobile stream, cancellation, and recovery tests.

## 3. Final verification

- [x] 3.1 Run upload-focused tests, `go test -race`, full `go test ./...`, `go vet ./...`, static checks, architecture checks, VFS stability, and localfs smoke.
- [x] 3.2 Validate this OpenSpec change strictly and confirm no unrelated files or behavior changed.
