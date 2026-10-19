package cli

import (
	"strings"
	"testing"
)

// TestUninstallAdvancedFlagsReveal verifies the uninstall command follows the
// help ladder: --purge stays hidden until --advanced is requested.
func TestUninstallAdvancedFlagsReveal(t *testing.T) {
	t.Parallel()

	def := flagsSection(runCommand(t, "uninstall", "--help"))
	if strings.Contains(def, "--purge") {
		t.Fatalf("advanced flag leaked into the default help flags:\n%s", def)
	}
	deep := flagsSection(runCommand(t, "uninstall", "--help", "--advanced"))
	if !strings.Contains(deep, "--purge") {
		t.Fatalf("deep help did not reveal --purge:\n%s", deep)
	}
}
