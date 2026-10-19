package selfupdate

import (
	"fmt"
	"runtime"
)

// ArchiveFormat names how a release packs its binary.
type ArchiveFormat string

const (
	// FormatTarGz is the gzip-compressed tar used on linux and darwin.
	FormatTarGz ArchiveFormat = "tar.gz"
	// FormatZip is the zip used on windows.
	FormatZip ArchiveFormat = "zip"
)

// Platform identifies the operating system and architecture an upgrade targets.
type Platform struct {
	GOOS   string
	GOARCH string
}

// CurrentPlatform returns the platform the running process belongs to.
func CurrentPlatform() Platform {
	return Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}

// ArchiveFormat returns the archive format qrypt publishes for the platform.
func (p Platform) ArchiveFormat() ArchiveFormat {
	if p.GOOS == "windows" {
		return FormatZip
	}
	return FormatTarGz
}

// InnerBinaryName returns the filename of the binary stored inside a release
// archive, which is not simply "qrypt" (for example qrypt-darwin-arm64).
func (p Platform) InnerBinaryName() string {
	name := fmt.Sprintf("qrypt-%s-%s", p.GOOS, p.GOARCH)
	if p.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// AssetArchiveName returns the release asset filename for a version and
// platform, for example "qrypt_0.5.3_darwin_arm64.tar.gz". The version carries
// no leading "v", matching the publisher.
func AssetArchiveName(v Version, p Platform) string {
	return fmt.Sprintf("qrypt_%d.%d.%d_%s_%s.%s", v.Major, v.Minor, v.Patch, p.GOOS, p.GOARCH, p.ArchiveFormat())
}

// ChecksumAssetName returns the release asset filename that carries the
// per-archive checksums, for example "qrypt_0.5.3_checksums.txt".
func ChecksumAssetName(v Version) string {
	return fmt.Sprintf("qrypt_%d.%d.%d_checksums.txt", v.Major, v.Minor, v.Patch)
}

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release is a published qrypt release as described by the releases API.
type Release struct {
	Tag        string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Assets     []Asset `json:"assets"`
}

// Version parses the release tag.
func (r Release) Version() (Version, error) {
	return ParseVersion(r.Tag)
}

// Stable reports whether the release is a published, non-prerelease release
// with a comparable version, which is what an upgrade may install.
func (r Release) Stable() bool {
	if r.Draft || r.Prerelease {
		return false
	}
	_, err := ParseVersion(r.Tag)
	return err == nil
}

// Asset returns the named asset and whether it is present.
func (r Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// SelectRelease returns the newest stable release from a list, comparing by
// version rather than by the order the API returned. It returns ErrNoRelease
// when the list holds no usable release.
func SelectRelease(releases []Release) (Release, error) {
	var (
		best        Release
		bestVersion Version
		found       bool
	)
	for _, r := range releases {
		if !r.Stable() {
			continue
		}
		v, err := r.Version()
		if err != nil {
			continue
		}
		if !found || v.Compare(bestVersion) > 0 {
			best, bestVersion, found = r, v, true
		}
	}
	if !found {
		return Release{}, ErrNoRelease
	}
	return best, nil
}
