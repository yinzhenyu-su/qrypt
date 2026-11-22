package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestDiagnosticsHelpSurfacesStaySeparated pins the boundary between the
// read-only debug group and the mutable probe group. They stay separate
// command groups, so the help surfaces must not bleed into each other.
func TestDiagnosticsHelpSurfacesStaySeparated(t *testing.T) {
	debugHelp := runCommand(t, "debug", "--help")
	// The debug group may mention read-only cache snapshots in prose; what it
	// must not advertise is the mutable cache/bench/fault command families.
	for _, forbidden := range []string{"  cache ", "  bench ", "  fault ", "  test "} {
		if strings.Contains(debugHelp, forbidden) {
			t.Fatalf("debug help must not advertise mutable command %q:\n%s", forbidden, debugHelp)
		}
	}

	probeHelp := runCommand(t, "probe", "--help")
	for _, want := range []string{"  cache ", "  bench ", "  fault ", "  test "} {
		if !strings.Contains(probeHelp, want) {
			t.Fatalf("probe help missing %q:\n%s", want, probeHelp)
		}
	}
	if !strings.Contains(strings.ToLower(probeHelp), "may modify") ||
		!strings.Contains(strings.ToLower(probeHelp), "mutable control socket") {
		t.Fatalf("probe help must warn that commands may write:\n%s", probeHelp)
	}
}

// TestRemovedFSEntriesAreHiddenFromHelpAndSchema checks the single-owner
// cleanup: dead flags are gone, and the moved crypt commands plus removed
// journal maintenance no longer appear in the fs help or schema.
func TestRemovedFSEntriesAreHiddenFromHelpAndSchema(t *testing.T) {
	fsHelp := runCommand(t, "fs", "--help")
	for _, gone := range []string{"--bwlimit", "--mount ", "crypt-encode", "crypt-decode", "journal replay"} {
		if strings.Contains(fsHelp, gone) {
			t.Fatalf("fs help still advertises removed entry %q:\n%s", gone, fsHelp)
		}
	}

	var schema commandSchema
	if err := json.Unmarshal([]byte(runCommand(t, "schema", "fs")), &schema); err != nil {
		t.Fatalf("schema fs: %v", err)
	}
	for _, sub := range schema.Commands {
		switch sub.Name {
		case "crypt-encode", "crypt-decode", "journal replay", "journal prune":
			t.Fatalf("schema still advertises removed fs command %q", sub.Name)
		}
	}
	for _, flag := range append(schema.Flags, schema.Advanced...) {
		if flag.Name == "mount" || strings.HasPrefix(flag.Name, "bwlimit") {
			t.Fatalf("schema still advertises removed fs flag --%s", flag.Name)
		}
	}

	// The top-level group is visible and carries both transforms.
	cryptHelp := runCommand(t, "crypt", "--help")
	for _, want := range []string{"encode", "decode"} {
		if !strings.Contains(cryptHelp, want) {
			t.Fatalf("crypt help missing %q:\n%s", want, cryptHelp)
		}
	}
}

// TestDeprecatedAliasesStayRunnable checks that hidden compatibility aliases
// are still addressable and warn once when used (they are removed only after
// one minor release).
func TestDeprecatedAliasesStayRunnable(t *testing.T) {
	aliasHelp := runCommand(t, "fs", "crypt-encode", "--help")
	if !strings.Contains(aliasHelp, "crypt-encode") {
		t.Fatalf("hidden alias help does not describe the alias:\n%s", aliasHelp)
	}

	base, err := os.MkdirTemp("", "qrypt-deprecated-alias-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	configPath, _ := writeCoreServeTestConfig(t, base)
	out, runErr := executeRoot(t, "fs", "crypt-encode", "--config", configPath, "hello.txt")
	if runErr == nil {
		t.Fatalf("expected the alias to fail without an encrypted mount, got output %q", out)
	}
	if !strings.Contains(out, "deprecated") {
		t.Fatalf("alias did not emit a deprecation notice:\n%s", out)
	}
}
