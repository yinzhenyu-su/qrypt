package fs

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	syncer "github.com/yinzhenyu/qrypt/pkg/syncer"
)

func TestPrintSyncSummaryLocalized(t *testing.T) {
	result := syncer.Result{
		Source:      "a",
		Destination: "b",
		Summary:     syncer.Summary{Adds: 1, Bytes: 10},
		Entries: []syncer.PlanEntry{
			{Path: "x", Action: syncer.ActionAdd, Reason: "missing", Bytes: 10},
		},
	}

	var english bytes.Buffer
	PrintSyncSummary(&english, result, i18n.New(i18n.LangEN))
	for _, want := range []string{
		"synced a -> b",
		"add: 1, update: 0, delete: 0, skip: 0, conflict: 0, failed: 0",
		"bytes: 10",
		"[add] x missing (10 bytes)",
	} {
		if !strings.Contains(english.String(), want) {
			t.Errorf("English sync summary missing %q:\n%s", want, english.String())
		}
	}

	var chinese bytes.Buffer
	PrintSyncSummary(&chinese, result, i18n.New(i18n.LangZHCN))
	for _, want := range []string{"已同步 a -> b", "新增: 1", "字节数：10", "[add] x missing（10 字节）"} {
		if !strings.Contains(chinese.String(), want) {
			t.Errorf("Chinese sync summary missing %q:\n%s", want, chinese.String())
		}
	}
}
