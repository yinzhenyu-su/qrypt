package selfupdate

import (
	"errors"
	"testing"
)

func TestAssetNamesMatchPublisher(t *testing.T) {
	t.Parallel()
	version := MustParseVersion("v0.5.3")
	cases := []struct {
		platform Platform
		archive  string
		inner    string
	}{
		{Platform{GOOS: "linux", GOARCH: "amd64"}, "qrypt_0.5.3_linux_amd64.tar.gz", "qrypt-linux-amd64"},
		{Platform{GOOS: "linux", GOARCH: "arm64"}, "qrypt_0.5.3_linux_arm64.tar.gz", "qrypt-linux-arm64"},
		{Platform{GOOS: "darwin", GOARCH: "arm64"}, "qrypt_0.5.3_darwin_arm64.tar.gz", "qrypt-darwin-arm64"},
		{Platform{GOOS: "windows", GOARCH: "amd64"}, "qrypt_0.5.3_windows_amd64.zip", "qrypt-windows-amd64.exe"},
	}
	for _, tc := range cases {
		if got := AssetArchiveName(version, tc.platform); got != tc.archive {
			t.Fatalf("AssetArchiveName(%+v) = %q, want %q", tc.platform, got, tc.archive)
		}
		if got := tc.platform.InnerBinaryName(); got != tc.inner {
			t.Fatalf("InnerBinaryName(%+v) = %q, want %q", tc.platform, got, tc.inner)
		}
	}
	if got := ChecksumAssetName(version); got != "qrypt_0.5.3_checksums.txt" {
		t.Fatalf("ChecksumAssetName = %q", got)
	}
}

func TestSelectReleaseSkipsPrereleasesAndDrafts(t *testing.T) {
	t.Parallel()
	releases := []Release{
		{Tag: "v0.6.0", Prerelease: true},
		{Tag: "v0.7.0", Draft: true},
		{Tag: "nightly"},
		{Tag: "v0.4.0"},
		{Tag: "v0.5.3"},
	}
	got, err := SelectRelease(releases)
	if err != nil {
		t.Fatalf("SelectRelease error: %v", err)
	}
	if got.Tag != "v0.5.3" {
		t.Fatalf("SelectRelease picked %q, want v0.5.3", got.Tag)
	}
}

func TestSelectReleaseErrorsWithoutStable(t *testing.T) {
	t.Parallel()
	_, err := SelectRelease([]Release{{Tag: "v0.6.0", Prerelease: true}})
	if !errors.Is(err, ErrNoRelease) {
		t.Fatalf("SelectRelease error = %v, want ErrNoRelease", err)
	}
}

func TestReleaseAssetLookup(t *testing.T) {
	t.Parallel()
	release := Release{Tag: "v0.5.3", Assets: []Asset{{Name: "qrypt_0.5.3_linux_amd64.tar.gz", URL: "http://example.test/a"}}}
	if asset, ok := release.Asset("qrypt_0.5.3_linux_amd64.tar.gz"); !ok || asset.URL == "" {
		t.Fatalf("Asset() = %+v, %v", asset, ok)
	}
	if _, ok := release.Asset("missing"); ok {
		t.Fatal("Asset(missing) reported present")
	}
}

func TestReleaseStable(t *testing.T) {
	t.Parallel()
	if !(Release{Tag: "v1.0.0"}).Stable() {
		t.Fatal("v1.0.0 must be stable")
	}
	for _, release := range []Release{{Tag: "v1.0.0", Prerelease: true}, {Tag: "v1.0.0", Draft: true}, {Tag: "dev"}} {
		if release.Stable() {
			t.Fatalf("%+v must not be stable", release)
		}
	}
}
