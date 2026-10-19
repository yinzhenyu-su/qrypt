//go:build !windows

package install

// BinaryName is the installed executable's name. It carries no version, so an
// in-place upgrade cannot leave a stale name behind. Unix has no mandatory
// executable suffix; Windows uses qrypt.exe.
const BinaryName = "qrypt"
