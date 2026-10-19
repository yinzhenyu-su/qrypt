package selfupdate

import "errors"

// Sentinel errors returned by the updater. Callers use errors.Is to map a
// failure to a message or an exit code.
var (
	// ErrInvalidVersion marks a string that is not a plain vMAJOR.MINOR.PATCH
	// version, such as a git-describe build identifier.
	ErrInvalidVersion = errors.New("invalid version")

	// ErrNotReleaseBuild marks a current build that no published release can be
	// compared against.
	ErrNotReleaseBuild = errors.New("current build is not a release version")

	// ErrNoRelease marks a release list without a usable stable release.
	ErrNoRelease = errors.New("no stable release found")

	// ErrVersionNotFound marks a requested release version that does not exist.
	ErrVersionNotFound = errors.New("release version not found")

	// ErrNoAsset marks a release that has no archive for the target platform.
	ErrNoAsset = errors.New("no release asset for this platform")

	// ErrNoChecksum marks a release that publishes no checksum for an asset.
	ErrNoChecksum = errors.New("release publishes no checksum for this asset")

	// ErrChecksumMismatch marks an archive whose sha256 does not match the
	// published checksum.
	ErrChecksumMismatch = errors.New("checksum mismatch")

	// ErrNotWritable marks an executable path the process cannot replace.
	ErrNotWritable = errors.New("executable is not writable")
)
