package journal

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintReportKeepsEnglishDiagnosticLabels(t *testing.T) {
	var out bytes.Buffer
	PrintReport(&out, DebugReport{
		Target:       DebugCacheTarget{Name: "local", Dir: "/tmp/cache"},
		Entries:      2,
		DirtyEntries: 1,
		CleanEntries: 1,
	})
	text := out.String()
	for _, want := range []string{"cache local", "journal entries=2", "dirty=1", "clean=1"} {
		if !strings.Contains(text, want) {
			t.Errorf("journal report missing English label %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"日志", "条目"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("journal report was localized to %q:\n%s", unwanted, text)
		}
	}
}
