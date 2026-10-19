package upgrade

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	buildinfo "github.com/yinzhenyu/qrypt/pkg/buildinfo"
	"github.com/yinzhenyu/qrypt/pkg/selfupdate"
)

// testRuntime is a minimal ErrorFactory; the real one lives in package cli,
// which imports this package and so cannot be imported back.
type testRuntime struct{}

type codedError struct {
	code int
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func (testRuntime) UsageError(_ *cobra.Command, format string, args ...any) error {
	return &codedError{code: 2, err: fmt.Errorf(format, args...)}
}

func (testRuntime) ExitError(code int, err error) error {
	return &codedError{code: code, err: err}
}

func execute(t *testing.T, info buildinfo.Info, args ...string) (string, string, error) {
	t.Helper()
	cmd := NewCommand(testRuntime{}, info)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errBuf.String(), err
}

// buildArchive packs a single-file release archive for the given platform.
func buildArchive(t *testing.T, platform selfupdate.Platform, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if platform.ArchiveFormat() == selfupdate.FormatZip {
		zw := zip.NewWriter(&buf)
		w, err := zw.Create(platform.InnerBinaryName())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	header := &tar.Header{Name: platform.InnerBinaryName(), Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// startReleaseServer serves a v0.5.3 release for the running platform.
func startReleaseServer(t *testing.T) *httptest.Server {
	t.Helper()
	platform := selfupdate.CurrentPlatform()
	version := selfupdate.MustParseVersion("v0.5.3")
	archiveName := selfupdate.AssetArchiveName(version, platform)
	checksumName := selfupdate.ChecksumAssetName(version)

	archive := buildArchive(t, platform, "new-binary")
	sum := sha256.Sum256(archive)
	blobs := map[string][]byte{
		archiveName:  archive,
		checksumName: []byte(fmt.Sprintf("%x  %s\n", sum, archiveName)),
	}

	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]selfupdate.Release{{
				Tag: "v0.5.3",
				Assets: []selfupdate.Asset{
					{Name: archiveName, URL: base + "/asset/" + archiveName},
					{Name: checksumName, URL: base + "/asset/" + checksumName},
				},
			}})
		case strings.HasPrefix(r.URL.Path, "/asset/"):
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
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

// withFixtureUpgrader points the command at a local release server.
func withFixtureUpgrader(t *testing.T, srv *httptest.Server) {
	t.Helper()
	previous := newUpgrader
	newUpgrader = func() *selfupdate.Upgrader {
		return &selfupdate.Upgrader{Client: &selfupdate.Client{BaseURL: srv.URL, Repo: "o/r", HTTP: srv.Client()}}
	}
	t.Cleanup(func() { newUpgrader = previous })
}

func TestCommandIsSelfDescribing(t *testing.T) {
	t.Parallel()
	cmd := NewCommand(testRuntime{}, buildinfo.Info{Version: "v0.5.3"})
	if strings.TrimSpace(cmd.Short) == "" {
		t.Fatal("command has no summary")
	}
	if strings.TrimSpace(cmd.Long) == "" {
		t.Fatal("command has no description")
	}
	examples := 0
	for _, line := range strings.Split(cmd.Example, "\n") {
		if strings.TrimSpace(line) != "" {
			examples++
		}
	}
	if examples == 0 {
		t.Fatal("command has no examples")
	}
}

func TestFlagsAreWired(t *testing.T) {
	t.Parallel()
	cmd := NewCommand(testRuntime{}, buildinfo.Info{Version: "v0.5.3"})
	for _, name := range []string{"check", "to", "force", "json"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("flag --%s is missing", name)
		}
	}
	for _, name := range []string{"to", "force"} {
		if flag := cmd.Flags().Lookup(name); !flag.Hidden {
			t.Fatalf("--%s must be hidden from the default help", name)
		}
	}
	for _, name := range []string{"check", "json"} {
		if flag := cmd.Flags().Lookup(name); flag.Hidden {
			t.Fatalf("--%s must be a common flag", name)
		}
	}
}

func TestInvalidToIsUsageError(t *testing.T) {
	t.Parallel()
	_, _, err := execute(t, buildinfo.Info{Version: "v0.4.0"}, "--to", "not-a-version")
	if err == nil {
		t.Fatal("expected an error for an unparsable --to")
	}
	if !strings.Contains(err.Error(), "expected a version") {
		t.Fatalf("error = %v, want a usage message about --to", err)
	}
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != 2 {
		t.Fatalf("error = %v, want usage exit code 2", err)
	}
}

func TestCheckJSONOutputsSingleObject(t *testing.T) {
	withFixtureUpgrader(t, startReleaseServer(t))

	stdout, stderr, err := execute(t, buildinfo.Info{Version: "v0.4.0"}, "--check", "--json")
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("--json must not interleave progress on stderr: %q", stderr)
	}
	var result selfupdate.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not a single JSON object: %v\n%s", err, stdout)
	}
	if result.Current != "v0.4.0" || result.Target != "v0.5.3" || result.Status != selfupdate.StatusChecked {
		t.Fatalf("result = %+v", result)
	}
	if result.Updated {
		t.Fatal("--check must not report an update")
	}
}

func TestCheckReportsProgressOnStderr(t *testing.T) {
	withFixtureUpgrader(t, startReleaseServer(t))

	stdout, stderr, err := execute(t, buildinfo.Info{Version: "v0.4.0"}, "--check")
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if !strings.Contains(stderr, "looking up") {
		t.Fatalf("progress steps are missing from stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "v0.4.0") || !strings.Contains(stdout, "v0.5.3") {
		t.Fatalf("stdout = %q, want both versions", stdout)
	}
}

func TestAlreadyUpToDateSucceeds(t *testing.T) {
	withFixtureUpgrader(t, startReleaseServer(t))

	stdout, _, err := execute(t, buildinfo.Info{Version: "v0.5.3"})
	if err != nil {
		t.Fatalf("up-to-date run returned an error: %v", err)
	}
	if !strings.Contains(stdout, "already up to date") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestRuntimeFailureUsesFailureExitCode(t *testing.T) {
	t.Parallel()
	// A development build is refused before any network call.
	_, _, err := execute(t, buildinfo.Info{Version: "dev"})
	if err == nil {
		t.Fatal("expected a runtime failure for a development build")
	}
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != 1 {
		t.Fatalf("error = %v, want runtime exit code 1", err)
	}
}
