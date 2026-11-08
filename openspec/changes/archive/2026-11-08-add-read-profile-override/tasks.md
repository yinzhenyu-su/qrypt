## 1. Config and schema

- [x] 1.1 Add `ReadProfileConfig` to `pkg/config`, wire `MountConfig.ReadProfile`, and implement `Config.ReadProfileFor`; verify with a table-driven config test that an absent table returns nil.
- [x] 1.2 Validate `streams` 0/1–32 and `window_chunks` 0/1–64 in `ValidateMount`; verify invalid values fail with mount name and range in the error.
- [x] 1.3 Add capability validation: `streams > 1` requires `CapabilityParallelRange` and rejects `CapabilityInternalParallelRead`; verify with fake drivers covering both cases.
- [x] 1.4 Add the `[mounts.read_profile]` schema definitions and generated `full-config.md`; verify `check-schema.py`, `gen-config-docs.py`, and `check-all.sh` schema/docs steps pass.

## 2. Effective profile resolution

- [x] 2.1 Add `drive.ReadProfileOfWithOverride` and keep `ReadProfileOf` behavior unchanged; verify precedence (override > driver > defaults) and nil override in `pkg/drive/read_profile_test.go`.
- [x] 2.2 Apply `ParallelReadLimiter` clamping in the new resolver and expose the clamp reason to callers; verify a limiter of 4 caps an override of 8.
- [x] 2.3 Verify `BulkWindowChunks` from the override flows into `PlanResolver` only for `bulk` intent and does not change `interactive`/`probe` windows; add plan tests.

## 3. VFS integration

- [x] 3.1 Add an optional read profile to `vfs.Options` and use it for `read.ReaderDeps.Profile`; verify a test VFS with an override produces the expected `ReadPlan`.
- [x] 3.2 Pass the mount override from `pkg/core.buildMount` into `vfs.New`; verify a core test with one overridden and one default mount keeps their profiles separate.
- [x] 3.3 Verify no override preserves the existing Koofr/Drime single-stream defaults and all current read tests remain green.

## 4. Direct read paths

- [x] 4.1 Add `drive.ReadParallelWithProfile` and make `ReadParallel` delegate to it; verify default behavior with existing `parallel_read` tests.
- [x] 4.2 Thread the source mount's effective profile through `drivecopy`; verify a direct-copy test observes the overridden stream count.
- [x] 4.3 Thread the same profile through `share fetch` fallback; verify its `drive.Read` call count and stream count match the mount override.

## 5. Diagnostics

- [x] 5.1 Add an effective read-profile block to the mount debug snapshot with streams, window, prefetch depth, threshold, source, and clamped flag; verify JSON serialization and redaction tests.
- [x] 5.2 Verify the snapshot reports `driver`, `mount_override`, and `driver_limiter` sources for three representative mounts.

## 6. Documentation and end-to-end verification

- [x] 6.1 Update `docs/for-developer/driver-development.md`, `docs/for-developer/architecture.md`, and `docs/for-user/full-config.md`; verify relative links and `git diff --check`.
- [x] 6.2 Add a short user example showing an opt-in Koofr `streams = 4` / `window_chunks = 16` override and the warning that it is environment-dependent; verify the generated full-config block matches the schema.
- [x] 6.3 Record the 2026-11-08 local 16/64 MiB Koofr/Drime matrix in `benchmark-baseline.md` under this change, including commands, medians, and the decision to keep defaults single-stream.
- [x] 6.4 Update `pkg/drive/read_profile.go` and `pkg/vfs/read/reader.go` comments, `docs/for-developer/driver-development.md`, and `.agents/skills/qrypt-read-tuning/SKILL.md` to say "driver default, optionally overridden per mount"; verify no remaining absolute "not user configuration" claim.
- [x] 6.5 Update `add-read-intent-policy` proposal/design wording to reference this follow-up rather than keeping the absolute "no new user configuration" claim; verify both changes still validate.
- [x] 6.6 Run `openspec validate add-read-profile-override --type change --strict` and confirm it passes.
- [x] 6.7 Run `scripts/ci/check-all.sh` with the CI Go version and confirm `== local CI check passed ==`.
- [x] 6.8 Run `gofmt -l` on all changed Go files and confirm no output.
