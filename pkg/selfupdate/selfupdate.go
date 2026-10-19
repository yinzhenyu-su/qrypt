package selfupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Outcome values reported in a Result.
const (
	StatusUpdated  = "updated"
	StatusUpToDate = "up_to_date"
	StatusChecked  = "checked"
)

// Options configures an upgrade run.
type Options struct {
	// Current is the running build's version, as reported by buildinfo.
	Current string

	// Target is the release to install; empty selects the newest release. A
	// non-empty value is a version such as "v0.5.0" and may be older than
	// Current, allowing a downgrade.
	Target string

	// Force proceeds even when Current is not a comparable release version.
	Force bool

	// Check reports the newest release without downloading or writing.
	Check bool

	// Platform defaults to the running platform.
	Platform Platform

	// Executable defaults to the running executable with symlinks resolved.
	Executable string
}

// Result describes what an upgrade run found and did.
type Result struct {
	Current string `json:"current"`
	Target  string `json:"target"`
	Status  string `json:"status"`
	Updated bool   `json:"updated"`
	Message string `json:"message"`
}

// Upgrader performs an upgrade. Client is injectable so tests can point it at
// a local release server; Logf receives progress lines.
type Upgrader struct {
	Client *Client
	Logf   func(format string, args ...any)
}

// NewUpgrader returns an upgrader backed by the default GitHub client.
func NewUpgrader() *Upgrader {
	return &Upgrader{Client: NewClient()}
}

// Run resolves, verifies, and installs the requested release.
func (u *Upgrader) Run(ctx context.Context, opts Options) (Result, error) {
	logf := u.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	platform := opts.Platform
	if platform.GOOS == "" || platform.GOARCH == "" {
		platform = CurrentPlatform()
	}

	current, currentErr := ParseVersion(opts.Current)
	if currentErr != nil && !opts.Force {
		return Result{}, fmt.Errorf("%w: %s", ErrNotReleaseBuild, opts.Current)
	}

	release, err := u.resolveRelease(ctx, opts, logf)
	if err != nil {
		return Result{}, err
	}
	releaseVersion, err := release.Version()
	if err != nil {
		return Result{}, fmt.Errorf("%w: release tag %q", ErrInvalidVersion, release.Tag)
	}

	result := Result{Current: opts.Current, Target: releaseVersion.String()}

	if !newerRequested(opts.Target, current, releaseVersion) {
		result.Status = StatusUpToDate
		result.Message = fmt.Sprintf("already up to date (%s)", opts.Current)
		return result, nil
	}

	if opts.Check {
		result.Status = StatusChecked
		result.Message = fmt.Sprintf("%s -> %s (check only)", opts.Current, releaseVersion)
		return result, nil
	}

	target, err := resolveTarget(opts.Executable)
	if err != nil {
		return Result{}, err
	}
	if err := u.install(ctx, release, releaseVersion, platform, target, logf); err != nil {
		return Result{}, err
	}

	result.Status = StatusUpdated
	result.Updated = true
	result.Message = fmt.Sprintf("upgraded %s -> %s", opts.Current, releaseVersion)
	return result, nil
}

func (u *Upgrader) resolveRelease(ctx context.Context, opts Options, logf func(string, ...any)) (Release, error) {
	if opts.Target != "" {
		version, err := ParseVersion(opts.Target)
		if err != nil {
			return Release{}, err
		}
		logf("looking up release %s", version)
		return u.client().ByTag(ctx, version.String())
	}
	logf("looking up the newest release")
	return u.client().Latest(ctx)
}

// install downloads, verifies, extracts, and swaps in the release binary.
func (u *Upgrader) install(ctx context.Context, release Release, version Version, platform Platform, target string, logf func(string, ...any)) error {
	assetName := AssetArchiveName(version, platform)
	asset, ok := release.Asset(assetName)
	if !ok {
		return fmt.Errorf("%w: %s for %s/%s", ErrNoAsset, assetName, platform.GOOS, platform.GOARCH)
	}
	checksumName := ChecksumAssetName(version)
	checksumAsset, ok := release.Asset(checksumName)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoChecksum, checksumName)
	}

	tmpDir, err := os.MkdirTemp("", "qrypt-selfupdate-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, assetName)
	logf("downloading %s", assetName)
	if _, err := u.downloader().Download(ctx, asset.URL, archivePath); err != nil {
		return err
	}

	logf("verifying %s", assetName)
	if err := u.verify(ctx, checksumAsset.URL, assetName, archivePath); err != nil {
		return err
	}

	logf("extracting %s", platform.InnerBinaryName())
	extracted := filepath.Join(tmpDir, "qrypt-new")
	if err := ExtractBinary(archivePath, platform.ArchiveFormat(), platform.InnerBinaryName(), extracted); err != nil {
		return err
	}

	logf("installing to %s", target)
	return installBinary(extracted, target)
}

// verify downloads the release checksum file and checks the archive against it.
func (u *Upgrader) verify(ctx context.Context, checksumURL, assetName, archivePath string) error {
	sumsPath := archivePath + ".checksums"
	if _, err := u.downloader().Download(ctx, checksumURL, sumsPath); err != nil {
		return err
	}
	f, err := os.Open(sumsPath)
	if err != nil {
		return err
	}
	defer f.Close()
	sums, err := ParseChecksums(f)
	if err != nil {
		return err
	}
	want, ok := sums[assetName]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoChecksum, assetName)
	}
	return VerifyChecksum(archivePath, want)
}

func (u *Upgrader) client() *Client {
	if u.Client != nil {
		return u.Client
	}
	return NewClient()
}

func (u *Upgrader) downloader() *Downloader {
	return &Downloader{HTTP: u.client().http()}
}

// newerRequested reports whether the run should proceed to install. The default
// upgrade only proceeds for a strictly newer release; an explicit --to request
// proceeds unless it names exactly the current version.
func newerRequested(target string, current, release Version) bool {
	if target != "" {
		return release.Compare(current) != 0
	}
	return release.Compare(current) > 0
}

func resolveTarget(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return ResolveExecutable()
}
