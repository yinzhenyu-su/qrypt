package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// testPlatform is fixed so fixtures are deterministic on any host.
var testPlatform = Platform{GOOS: "linux", GOARCH: "amd64"}

// fixtureRelease builds a release plus the blob bytes served for its assets.
func fixtureRelease(t *testing.T, base, tag, content string) (Release, map[string][]byte) {
	t.Helper()
	version := MustParseVersion(tag)
	archiveName := AssetArchiveName(version, testPlatform)
	checksumName := ChecksumAssetName(version)
	archive := tarGzBytes(t, testPlatform.InnerBinaryName(), content)
	sum := sha256.Sum256(archive)
	release := Release{Tag: tag, Assets: []Asset{
		{Name: archiveName, URL: base + "/asset/" + archiveName},
		{Name: checksumName, URL: base + "/asset/" + checksumName},
	}}
	blobs := map[string][]byte{
		archiveName:  archive,
		checksumName: []byte(fmt.Sprintf("%x  %s\n", sum, archiveName)),
	}
	return release, blobs
}

// startFixture serves the built releases and their blobs, counting asset
// downloads so a test can assert that an operation did not download.
func startFixture(t *testing.T, build func(base string) ([]Release, map[string][]byte)) (*httptest.Server, *int32) {
	t.Helper()
	var (
		downloads int32
		releases  []Release
		blobs     map[string][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			tag := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			for _, release := range releases {
				if release.Tag == tag {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(release)
					return
				}
			}
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/releases"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(releases)
		case strings.HasPrefix(r.URL.Path, "/asset/"):
			atomic.AddInt32(&downloads, 1)
			blob, ok := blobs[strings.TrimPrefix(r.URL.Path, "/asset/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(blob)
		default:
			http.NotFound(w, r)
		}
	}))
	releases, blobs = build(srv.URL)
	t.Cleanup(srv.Close)
	return srv, &downloads
}

func newTestUpgrader(srv *httptest.Server) *Upgrader {
	return &Upgrader{Client: &Client{BaseURL: srv.URL, Repo: "o/r", HTTP: srv.Client()}}
}

func TestRunInstallsNewerRelease(t *testing.T) {
	t.Parallel()
	srv, downloads := startFixture(t, func(base string) ([]Release, map[string][]byte) {
		release, blobs := fixtureRelease(t, base, "v0.5.3", "new-binary")
		return []Release{release}, blobs
	})
	target := filepath.Join(t.TempDir(), "qrypt")
	writeFile(t, target, "old-binary", 0o755)

	result, err := newTestUpgrader(srv).Run(context.Background(), Options{
		Current:    "v0.4.0",
		Platform:   testPlatform,
		Executable: target,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !result.Updated || result.Status != StatusUpdated {
		t.Fatalf("result = %+v, want an update", result)
	}
	if !strings.Contains(result.Message, "v0.4.0") || !strings.Contains(result.Message, "v0.5.3") {
		t.Fatalf("message = %q, want both versions", result.Message)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-binary" {
		t.Fatalf("installed content = %q, want new-binary", got)
	}
	if atomic.LoadInt32(downloads) == 0 {
		t.Fatal("expected at least one download")
	}
}

func TestRunCheckDownloadsNothing(t *testing.T) {
	t.Parallel()
	srv, downloads := startFixture(t, func(base string) ([]Release, map[string][]byte) {
		release, blobs := fixtureRelease(t, base, "v0.5.3", "new-binary")
		return []Release{release}, blobs
	})
	target := filepath.Join(t.TempDir(), "qrypt")
	writeFile(t, target, "old-binary", 0o755)

	result, err := newTestUpgrader(srv).Run(context.Background(), Options{
		Current:    "v0.4.0",
		Check:      true,
		Platform:   testPlatform,
		Executable: target,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Updated || result.Status != StatusChecked {
		t.Fatalf("result = %+v, want a check-only result", result)
	}
	if got := atomic.LoadInt32(downloads); got != 0 {
		t.Fatalf("check downloaded %d assets, want 0", got)
	}
	if got, _ := os.ReadFile(target); string(got) != "old-binary" {
		t.Fatalf("check modified the target: %q", got)
	}
}

func TestRunAlreadyUpToDate(t *testing.T) {
	t.Parallel()
	srv, downloads := startFixture(t, func(base string) ([]Release, map[string][]byte) {
		release, blobs := fixtureRelease(t, base, "v0.5.3", "new-binary")
		return []Release{release}, blobs
	})
	target := filepath.Join(t.TempDir(), "qrypt")
	writeFile(t, target, "old-binary", 0o755)

	result, err := newTestUpgrader(srv).Run(context.Background(), Options{
		Current:    "v0.5.3",
		Platform:   testPlatform,
		Executable: target,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Updated || result.Status != StatusUpToDate {
		t.Fatalf("result = %+v, want up to date", result)
	}
	if got := atomic.LoadInt32(downloads); got != 0 {
		t.Fatalf("up-to-date run downloaded %d assets, want 0", got)
	}
	if got, _ := os.ReadFile(target); string(got) != "old-binary" {
		t.Fatalf("up-to-date run modified the target: %q", got)
	}
}

func TestRunRefusesNonReleaseBuild(t *testing.T) {
	t.Parallel()
	srv, _ := startFixture(t, func(base string) ([]Release, map[string][]byte) {
		release, blobs := fixtureRelease(t, base, "v0.5.3", "new-binary")
		return []Release{release}, blobs
	})
	target := filepath.Join(t.TempDir(), "qrypt")
	writeFile(t, target, "old-binary", 0o755)

	_, err := newTestUpgrader(srv).Run(context.Background(), Options{
		Current:    "v0.5.3-10-g9d16c54",
		Platform:   testPlatform,
		Executable: target,
	})
	if !errors.Is(err, ErrNotReleaseBuild) {
		t.Fatalf("Run error = %v, want ErrNotReleaseBuild", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old-binary" {
		t.Fatalf("refused run modified the target: %q", got)
	}

	result, err := newTestUpgrader(srv).Run(context.Background(), Options{
		Current:    "v0.5.3-10-g9d16c54",
		Force:      true,
		Platform:   testPlatform,
		Executable: target,
	})
	if err != nil {
		t.Fatalf("forced Run error: %v", err)
	}
	if !result.Updated {
		t.Fatalf("forced result = %+v, want an update", result)
	}
}

func TestRunChecksumMismatchAborts(t *testing.T) {
	t.Parallel()
	srv, _ := startFixture(t, func(base string) ([]Release, map[string][]byte) {
		release, blobs := fixtureRelease(t, base, "v0.5.3", "new-binary")
		// Replace the checksum file with a wrong digest for the archive.
		blobs[ChecksumAssetName(MustParseVersion("v0.5.3"))] = []byte(
			strings.Repeat("0", 64) + "  " + AssetArchiveName(MustParseVersion("v0.5.3"), testPlatform) + "\n")
		return []Release{release}, blobs
	})
	target := filepath.Join(t.TempDir(), "qrypt")
	writeFile(t, target, "old-binary", 0o755)

	_, err := newTestUpgrader(srv).Run(context.Background(), Options{
		Current:    "v0.4.0",
		Platform:   testPlatform,
		Executable: target,
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Run error = %v, want ErrChecksumMismatch", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old-binary" {
		t.Fatalf("a mismatched checksum modified the target: %q", got)
	}
}

func TestRunMissingChecksumEntryAborts(t *testing.T) {
	t.Parallel()
	srv, _ := startFixture(t, func(base string) ([]Release, map[string][]byte) {
		release, blobs := fixtureRelease(t, base, "v0.5.3", "new-binary")
		// A checksum file that names a different asset.
		blobs[ChecksumAssetName(MustParseVersion("v0.5.3"))] = []byte(strings.Repeat("a", 64) + "  qrypt_0.5.3_plan9_riscv.tar.gz\n")
		return []Release{release}, blobs
	})
	target := filepath.Join(t.TempDir(), "qrypt")
	writeFile(t, target, "old-binary", 0o755)

	_, err := newTestUpgrader(srv).Run(context.Background(), Options{
		Current:    "v0.4.0",
		Platform:   testPlatform,
		Executable: target,
	})
	if !errors.Is(err, ErrNoChecksum) {
		t.Fatalf("Run error = %v, want ErrNoChecksum", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old-binary" {
		t.Fatalf("a missing checksum entry modified the target: %q", got)
	}
}

func TestRunMissingAssetAborts(t *testing.T) {
	t.Parallel()
	srv, _ := startFixture(t, func(base string) ([]Release, map[string][]byte) {
		return []Release{{Tag: "v0.5.3"}}, map[string][]byte{}
	})
	target := filepath.Join(t.TempDir(), "qrypt")
	writeFile(t, target, "old-binary", 0o755)

	_, err := newTestUpgrader(srv).Run(context.Background(), Options{
		Current:    "v0.4.0",
		Platform:   testPlatform,
		Executable: target,
	})
	if !errors.Is(err, ErrNoAsset) {
		t.Fatalf("Run error = %v, want ErrNoAsset", err)
	}
}

func TestRunToVersionAllowsDowngrade(t *testing.T) {
	t.Parallel()
	srv, _ := startFixture(t, func(base string) ([]Release, map[string][]byte) {
		older, olderBlobs := fixtureRelease(t, base, "v0.4.0", "older-binary")
		newer, newerBlobs := fixtureRelease(t, base, "v0.5.3", "newer-binary")
		for name, blob := range newerBlobs {
			olderBlobs[name] = blob
		}
		return []Release{newer, older}, olderBlobs
	})
	target := filepath.Join(t.TempDir(), "qrypt")
	writeFile(t, target, "old-binary", 0o755)

	result, err := newTestUpgrader(srv).Run(context.Background(), Options{
		Current:    "v0.5.3",
		Target:     "v0.4.0",
		Platform:   testPlatform,
		Executable: target,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Target != "v0.4.0" {
		t.Fatalf("result.Target = %q, want v0.4.0", result.Target)
	}
	if got, _ := os.ReadFile(target); string(got) != "older-binary" {
		t.Fatalf("downgrade content = %q, want older-binary", got)
	}
}

func TestRunToMissingVersionFails(t *testing.T) {
	t.Parallel()
	srv, _ := startFixture(t, func(base string) ([]Release, map[string][]byte) {
		release, blobs := fixtureRelease(t, base, "v0.5.3", "new-binary")
		return []Release{release}, blobs
	})
	target := filepath.Join(t.TempDir(), "qrypt")
	writeFile(t, target, "old-binary", 0o755)

	_, err := newTestUpgrader(srv).Run(context.Background(), Options{
		Current:    "v0.4.0",
		Target:     "v9.9.9",
		Platform:   testPlatform,
		Executable: target,
	})
	if !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("Run error = %v, want ErrVersionNotFound", err)
	}
}
