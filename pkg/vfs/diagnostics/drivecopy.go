package diagnostics

type DriverCopySource interface {
	DriverProvider
	DebugResolver
	DebugSnapshot() DebugSnapshot
}
