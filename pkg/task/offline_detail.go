package task

// OfflineDetailKey is the Task.Detail key under which provider-backed offline
// metadata lives. It is a stable wire key: the persistent store hashes the
// value under this key so a provider identifier or phase change is journaled
// even when the task state does not move, and clients read it for provider
// diagnostics. The value shape is owned by pkg/core (Core exposes the task API).
const OfflineDetailKey = "offline"
