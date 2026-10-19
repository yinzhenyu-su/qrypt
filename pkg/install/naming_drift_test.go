package install

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/selfupdate"
)

// installerScript and installerPowerShellScript are the bootstrap scripts,
// relative to this package directory (go test runs with the package directory
// as the working directory).
const (
	installerScript           = "../../scripts/install.sh"
	installerPowerShellScript = "../../scripts/install.ps1"
)

// TestInstallerNamingMatchesUpdater fails when scripts/install.sh and
// pkg/selfupdate disagree about how a release is named: the asset file, the
// checksum file, and the in-archive binary name. The script exposes its rule
// through --print-manifest precisely so this contract can be checked.
func TestInstallerNamingMatchesUpdater(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scripts/install.sh targets linux and darwin only")
	}

	version := selfupdate.MustParseVersion("v0.5.3")
	platforms := []selfupdate.Platform{
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
		{GOOS: "windows", GOARCH: "amd64"},
	}

	for _, platform := range platforms {
		platform := platform
		t.Run(platform.GOOS+"/"+platform.GOARCH, func(t *testing.T) {
			got := runManifest(t, "v0.5.3", platform.GOOS, platform.GOARCH)

			if want := selfupdate.AssetArchiveName(version, platform); got["asset"] != want {
				t.Errorf("install.sh asset = %q, updater = %q", got["asset"], want)
			}
			if want := selfupdate.ChecksumAssetName(version); got["checksum"] != want {
				t.Errorf("install.sh checksum = %q, updater = %q", got["checksum"], want)
			}
			if want := platform.InnerBinaryName(); got["inner"] != want {
				t.Errorf("install.sh inner binary = %q, updater = %q", got["inner"], want)
			}
		})
	}
}

// TestInstallerLayoutMatchesConstants covers the paths and markers that the
// uninstall command depends on.
func TestInstallerLayoutMatchesConstants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scripts/install.sh targets linux and darwin only")
	}
	got := runManifest(t, "v0.5.3", "linux", "amd64")
	checks := map[string]string{
		"real_dir":     RealDir,
		"link_dir":     LinkDir,
		"marker_begin": MarkerBegin,
		"marker_end":   MarkerEnd,
	}
	for key, want := range checks {
		if got[key] != want {
			t.Errorf("install.sh %s = %q, want %q", key, got[key], want)
		}
	}
}

// TestPowerShellInstallerNamingMatchesUpdater applies the same contract to
// scripts/install.ps1. The Windows CI job runs it with pwsh available; other
// environments skip it rather than requiring PowerShell.
func TestPowerShellInstallerNamingMatchesUpdater(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not installed; the Windows CI job runs this check")
	}

	version := selfupdate.MustParseVersion("v0.5.3")
	platforms := []selfupdate.Platform{
		{GOOS: "windows", GOARCH: "amd64"},
		{GOOS: "windows", GOARCH: "arm64"},
	}

	for _, platform := range platforms {
		platform := platform
		t.Run(platform.GOOS+"/"+platform.GOARCH, func(t *testing.T) {
			got := runPowerShellManifest(t, pwsh, "v0.5.3", platform.GOOS, platform.GOARCH)

			if want := selfupdate.AssetArchiveName(version, platform); got["asset"] != want {
				t.Errorf("install.ps1 asset = %q, updater = %q", got["asset"], want)
			}
			if want := selfupdate.ChecksumAssetName(version); got["checksum"] != want {
				t.Errorf("install.ps1 checksum = %q, updater = %q", got["checksum"], want)
			}
			if want := platform.InnerBinaryName(); got["inner"] != want {
				t.Errorf("install.ps1 inner binary = %q, updater = %q", got["inner"], want)
			}
			if got["real_dir"] != RealDir {
				t.Errorf("install.ps1 real_dir = %q, want %q", got["real_dir"], RealDir)
			}
			if got["marker_name"] != InstallMarkerName {
				t.Errorf("install.ps1 marker_name = %q, want %q", got["marker_name"], InstallMarkerName)
			}
			// BinaryName is the host's constant, so compare the Windows name
			// only where the host is Windows; the naming contract above is
			// platform-parameterized by -ManifestOS.
			if runtime.GOOS != "windows" {
				return
			}
			if got["binary_name"] != BinaryName {
				t.Errorf("install.ps1 binary_name = %q, want %q", got["binary_name"], BinaryName)
			}
		})
	}
}

func runPowerShellManifest(t *testing.T, pwsh, version, goos, goarch string) map[string]string {
	t.Helper()
	script, err := filepath.Abs(installerPowerShellScript)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", script,
		"-PrintManifest", "-Version", version,
		"-ManifestOS", goos, "-ManifestArch", goarch).CombinedOutput()
	if err != nil {
		t.Fatalf("install.ps1 -PrintManifest %s %s %s: %v\n%s", version, goos, goarch, err, out)
	}
	manifest := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(strings.TrimPrefix(string(out), "\ufeff")), "\n") {
		line = strings.TrimSuffix(line, "\r")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("unexpected manifest line %q", line)
		}
		manifest[key] = value
	}
	return manifest
}

func runManifest(t *testing.T, version, goos, goarch string) map[string]string {
	t.Helper()
	script, err := filepath.Abs(installerScript)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", script, "--print-manifest", version, goos, goarch).CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh --print-manifest %s %s %s: %v\n%s", version, goos, goarch, err, out)
	}
	manifest := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("unexpected manifest line %q", line)
		}
		manifest[key] = value
	}
	return manifest
}
