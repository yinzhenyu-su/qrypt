package diagnostics

import "context"

type DebugResolver interface {
	DebugResolve(ctx context.Context, path string, includeRemoteName bool) (DebugResolveInfo, error)
}

type DebugConsistencyChecker interface {
	DebugConsistency(ctx context.Context, path string) (ConsistencyReport, error)
}

type DebugStagingInspector interface {
	DebugStaging(ctx context.Context, path string) (DebugStagingReport, error)
}

type DebugMountSnapshotter interface {
	DebugSnapshotForMounts(mountNames []string) DebugSnapshot
}

type DebugSnapshotProvider interface {
	DebugSnapshot() DebugSnapshot
}

type MountHealthChecker interface {
	MountHealth(ctx context.Context, mountName string) ([]MountHealth, error)
}

type RemoteIDResolver interface {
	DebugResolveByRemoteID(ctx context.Context, remoteID string) (*DebugResolveInfo, string, error)
}
