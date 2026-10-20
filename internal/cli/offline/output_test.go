package offline

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

func localizedCommand(lang string) *cobra.Command {
	cmd := &cobra.Command{Use: "offline"}
	i18n.Attach(cmd, lang)
	return cmd
}

func TestWriteTaskTextLocalized(t *testing.T) {
	var out bytes.Buffer
	cmd := localizedCommand(i18n.LangZHCN)
	cmd.SetOut(&out)
	if err := writeTaskText(cmd, task.Task{
		ID:    "t1",
		Type:  task.TypeOfflineDownload,
		State: task.StateRunning,
		Mount: "quark",
		Name:  "movie",
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID：t1", "类型：offline_download", "状态：running", "挂载：quark", "名称：movie"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("Chinese task output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "id: t1") {
		t.Errorf("Chinese task output kept English labels:\n%s", out.String())
	}
}

func TestWriteTaskListLocalizedEmpty(t *testing.T) {
	var out bytes.Buffer
	cmd := localizedCommand(i18n.LangZHCN)
	cmd.SetOut(&out)
	if err := writeTaskList(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "没有离线任务") {
		t.Fatalf("Chinese empty list output:\n%s", out.String())
	}
}

func TestWritePreviewLocalized(t *testing.T) {
	var out bytes.Buffer
	cmd := localizedCommand(i18n.LangZHCN)
	cmd.SetOut(&out)
	if err := writePreview(cmd, control.OfflinePreviewResult{
		Source:      "magnet:?xt=urn:btih:...",
		Title:       "movie",
		TotalSize:   1024,
		InstantSave: true,
		Mount:       "quark",
		ParseUsable: false,
		ParseError:  "missing mount",
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"来源：magnet", "标题：movie", "总大小：1.00 KiB", "秒传：true", "挂载：quark", "解析落地目录不可用"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("Chinese preview output missing %q:\n%s", want, out.String())
		}
	}
}

func TestWriteTaskListKeepsEnglishHeader(t *testing.T) {
	var out bytes.Buffer
	cmd := localizedCommand(i18n.LangZHCN)
	cmd.SetOut(&out)
	if err := writeTaskList(cmd, []task.Task{{
		ID:    "t1",
		Type:  task.TypeOfflineDownload,
		State: task.StateRunning,
		Mount: "quark",
		Name:  "movie",
	}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "TYPE", "STATE", "MOUNT", "NAME"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("task list missing English header %q:\n%s", want, out.String())
		}
	}
	for _, unwanted := range []string{"类型", "状态", "挂载"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("task list header was localized to %q:\n%s", unwanted, out.String())
		}
	}
}
