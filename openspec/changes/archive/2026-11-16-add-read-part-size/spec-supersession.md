# Spec supersession note

This change was authored in the `[mounts.read_profile]` / 512 KiB chunk-count
vocabulary. While it was still open, `unify-read-policy-execution` (archived
2026-11-16) rewrote every requirement its deltas touch into the `[mounts.read]`
bytes vocabulary, and `optimize-seek-read-profiles` (same date) added the
per-stage suggestion requirements.

The implementation this change describes did ship, as `bulk.part_size` and
`bulk.min_parallel_size`, which is what `pkg/config/config.go` now tells
operators migrating off `mounts.read_profile`. Its spec content is already in the
main specs:

- `Per-mount read profile override`, `Validation and error reporting`, and
  `Effective profile observability` exist in
  `openspec/specs/read-profile-config/spec.md` in the new vocabulary, including
  the scenarios `openspec validate` reports as missing from this delta
  (`Legacy table has migration guidance`, `Explicit auto clears fixed
  splitting`, `Planned streams differ from grant`).
- The added direct-read requirement is covered by `Override scope and intent`
  → `Direct read path uses the same profile` in `read-profile-config`, and by
  `直读路径使用相同的并行策略` in `read-pipeline`.
- The split semantics below ("余量并入最后一片，保证窗口仍被完整覆盖") were
  later corrected by `read-pipeline` → `分片上限与并行数量独立`, which requires
  bounded windows to be cut into tasks no larger than the part-size cap and
  forbids the last part from absorbing a remainder beyond that cap.

No delta was synced when this change was archived. Applying these blocks would
have reverted the `mounts.read` migration and reintroduced `mounts.read_profile`,
`part_chunks`, and `min_split_chunks`.
