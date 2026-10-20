package fs

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	drivecopy "github.com/yinzhenyu/qrypt/pkg/vfs/drivecopy"
)

func TestPrintCopySummaryLocalized(t *testing.T) {
	result := &drivecopy.DriverCopyResult{
		SourcePath:  "/a",
		DestPath:    "/b",
		SourceMount: "src",
		DestMount:   "dst",
		Bytes:       12,
		Duration:    "1s",
	}

	var english bytes.Buffer
	PrintCopySummary(&english, i18n.New(i18n.LangEN), result)
	for _, want := range []string{"copied /a -> /b", "mounts: src -> dst", "bytes: 12", "duration: 1s"} {
		if !strings.Contains(english.String(), want) {
			t.Errorf("English copy summary missing %q:\n%s", want, english.String())
		}
	}

	var chinese bytes.Buffer
	PrintCopySummary(&chinese, i18n.New(i18n.LangZHCN), result)
	for _, want := range []string{"已复制 /a -> /b", "挂载：src -> dst", "字节数：12", "耗时：1s"} {
		if !strings.Contains(chinese.String(), want) {
			t.Errorf("Chinese copy summary missing %q:\n%s", want, chinese.String())
		}
	}
}

func TestPrintCopyDirSummaryLocalized(t *testing.T) {
	result := &drivecopy.DriverCopyDirResult{
		SourcePath: "/a",
		DestPath:   "/b",
		Copied:     2,
		Skipped:    1,
		Failed:     0,
		Bytes:      12,
		Duration:   "1s",
	}

	var chinese bytes.Buffer
	PrintCopyDirSummary(&chinese, i18n.New(i18n.LangZHCN), result)
	for _, want := range []string{"已复制目录 /a -> /b", "已复制文件：2", "跳过文件：1", "失败文件：0", "字节数：12", "耗时：1s"} {
		if !strings.Contains(chinese.String(), want) {
			t.Errorf("Chinese directory copy summary missing %q:\n%s", want, chinese.String())
		}
	}
}

func TestPrintPendingVerboseKeepsEnglishHeader(t *testing.T) {
	var out bytes.Buffer
	PrintPendingVerbose(&out, []vfs.PendingUpload{{Path: "/a", Size: 1, LocalPath: "/tmp/a"}})
	text := out.String()
	for _, want := range []string{"PATH", "SIZE", "LOCAL", "STAGING", "RETRY"} {
		if !strings.Contains(text, want) {
			t.Errorf("pending table missing English header %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"路径", "大小"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("pending table header was localized to %q:\n%s", unwanted, text)
		}
	}
}
