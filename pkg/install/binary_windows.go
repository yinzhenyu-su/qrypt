//go:build windows

package install

// BinaryName is the installed executable's name. It carries no version, so an
// in-place upgrade cannot leave a stale name behind.
const BinaryName = "qrypt.exe"
