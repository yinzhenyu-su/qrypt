package diagnostics

import "github.com/yinzhenyu/qrypt/pkg/drive"

// NamedDriver binds a mount name to its underlying driver reference for
// driver-level debugging.
type NamedDriver struct {
	Name        string
	Driver      drive.Driver
	TestEnabled bool
}

// DriverProvider is implemented by VFS and Namespace to expose the
// underlying driver references for driver-level debugging.
type DriverProvider interface {
	Drivers() []NamedDriver
}
