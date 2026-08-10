// Exported diagnostics DTO aliases. The implementations live in
// internal/vfs/diagnostics; pkg/vfs keeps these names for external
// callers (control server, CLI, mobile) so they never import the
// internal package.
package vfs

import (
	"github.com/yinzhenyu/qrypt/internal/vfs/diagnostics"
)

type DebugSnapshot = diagnostics.DebugSnapshot
type DebugProcess = diagnostics.DebugProcess
type MountSnapshot = diagnostics.MountSnapshot
type DebugCacheSnapshot = diagnostics.DebugCacheSnapshot
type MountSnapshotIdentity = diagnostics.MountSnapshotIdentity
type MountSnapshotQueues = diagnostics.MountSnapshotQueues
type MountSnapshotOverlay = diagnostics.MountSnapshotOverlay
type MountSnapshotUploads = diagnostics.MountSnapshotUploads
type MountSnapshotEvents = diagnostics.MountSnapshotEvents
type MountSnapshotRuntime = diagnostics.MountSnapshotRuntime
type DebugTimer = diagnostics.DebugTimer
type DebugDeletedEntry = diagnostics.DebugDeletedEntry
type DebugOverlayOp = diagnostics.DebugOverlayOp
type DebugCopyHidden = diagnostics.DebugCopyHidden
type DebugStagingReport = diagnostics.DebugStagingReport
type DebugStagingMount = diagnostics.DebugStagingMount
type DebugStagingFile = diagnostics.DebugStagingFile
type DebugResolveInfo = diagnostics.DebugResolveInfo
type ConsistencyReport = diagnostics.ConsistencyReport
type DebugActiveMount = diagnostics.DebugActiveMount
type DebugActiveProvider = diagnostics.DebugActiveProvider
type MountHealth = diagnostics.MountHealth
type MountHealthOp = diagnostics.MountHealthOp
type NamedDriver = diagnostics.NamedDriver
type DriverProvider = diagnostics.DriverProvider

// DebugSnapshotSchemaVersion aliases the diagnostics schema version.
const DebugSnapshotSchemaVersion = diagnostics.DebugSnapshotSchemaVersion
