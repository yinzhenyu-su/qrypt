package fs

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
)

func TestProgressBarUsesLocalizer(t *testing.T) {
	var out bytes.Buffer
	bar := newProgressBar(2, i18n.New(i18n.LangZHCN), &out)
	bar.fileStarted("a")
	bar.fileDownloaded("a")
	bar.fileSkipped("b")
	bar.done("dir")

	text := out.String()
	for _, want := range []string{"跳过 1 个", "已下载 1 个，跳过 1 个"} {
		if !strings.Contains(text, want) {
			t.Errorf("localized progress missing %q:\n%s", want, text)
		}
	}
}

func TestProgressBarEnglishOutput(t *testing.T) {
	var out bytes.Buffer
	bar := newProgressBar(1, i18n.New(i18n.LangEN), &out)
	bar.fileDownloaded("a")
	bar.done("dir")

	text := out.String()
	for _, want := range []string{"1 downloaded"} {
		if !strings.Contains(text, want) {
			t.Errorf("English progress missing %q:\n%s", want, text)
		}
	}
}
