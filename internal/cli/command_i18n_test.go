package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
)

func runLocalizedCommand(t *testing.T, lang string, args ...string) string {
	t.Helper()
	root := NewRootCommand("--lang", lang)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute %s %v: %v\n%s", lang, args, err, out.String())
	}
	return out.String()
}

func TestCommandTextCatalogCoverage(t *testing.T) {
	root := NewRootCommand()
	en := i18n.New(i18n.LangEN)
	zh := i18n.New(i18n.LangZHCN)

	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Hidden {
			return
		}
		key := i18n.CommandKey(cmd)
		for field, value := range map[string]string{
			"short":   cmd.Short,
			"long":    cmd.Long,
			"example": cmd.Example,
		} {
			if value == "" {
				continue
			}
			catalogKey := "cmd." + key + "." + field
			if en.T(catalogKey) == catalogKey {
				t.Errorf("English catalog missing %s", catalogKey)
			}
			if zh.T(catalogKey) == catalogKey {
				t.Errorf("Chinese catalog missing %s", catalogKey)
			}
		}
		cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
			if flag.Usage == "" {
				return
			}
			catalogKey := "flag." + key + "." + flag.Name + ".usage"
			switch flag.Name {
			case "help", "version":
				catalogKey = "flag." + flag.Name + ".usage"
			}
			if en.T(catalogKey) == catalogKey {
				t.Errorf("English catalog missing %s", catalogKey)
			}
			if zh.T(catalogKey) == catalogKey {
				t.Errorf("Chinese catalog missing %s", catalogKey)
			}
		})
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
}

func TestCommandHelpLanguage(t *testing.T) {
	english := NewRootCommand()
	if !strings.Contains(english.Short, "Mounts") {
		t.Fatalf("English root summary = %q", english.Short)
	}

	chinese := NewRootCommand("--lang", "zh-CN")
	if !strings.Contains(chinese.Short, "云盘") {
		t.Fatalf("Chinese root summary = %q", chinese.Short)
	}
	fs, _, err := chinese.Find([]string{"fs", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fs.Short, "目录") {
		t.Fatalf("Chinese fs list summary = %q", fs.Short)
	}
}

func TestCommandHelpLanguageFromEnvironment(t *testing.T) {
	t.Setenv("QRYPT_LANG", "zh-CN")
	root := NewRootCommand("qrypt")
	if !strings.Contains(root.Short, "云盘") {
		t.Fatalf("environment-selected root summary = %q", root.Short)
	}
}

func TestCommandHelpUnsupportedLanguageFallsBack(t *testing.T) {
	root := NewRootCommand("--lang", "fr")
	if !strings.Contains(root.Short, "Mounts") {
		t.Fatalf("unsupported locale root summary = %q", root.Short)
	}
}

func TestCommandSchemaStaysEnglishUnderChineseLocale(t *testing.T) {
	root := NewRootCommand("--lang", "zh-CN")
	schema := schemaFor(root)
	if !strings.Contains(schema.Summary, "Mounts") {
		t.Fatalf("schema summary = %q, want English", schema.Summary)
	}
	fs, _, err := root.Find([]string{"fs", "list"})
	if err != nil {
		t.Fatal(err)
	}
	list := schemaFor(fs)
	if !strings.Contains(list.Summary, "List") {
		t.Fatalf("fs list schema summary = %q, want English", list.Summary)
	}
}

func TestCommandHelpScaffoldingIsLocalized(t *testing.T) {
	out := runLocalizedCommand(t, "zh-CN", "fs", "list", "--help")
	for _, want := range []string{"用法:", "示例:", "选项:", "全局选项:"} {
		if !strings.Contains(out, want) {
			t.Errorf("Chinese help missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Usage:", "Examples:", "Global Flags:"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("Chinese help still contains %q:\n%s", unwanted, out)
		}
	}
	group := runLocalizedCommand(t, "zh-CN", "fs", "--help")
	if !strings.Contains(group, "使用 \"qrypt fs [command] --help\" 查看子命令的详细信息。") {
		t.Errorf("Chinese group help missing localized more-information line:\n%s", group)
	}
}

func TestBuiltinHelpCommandsAreLocalized(t *testing.T) {
	help := runLocalizedCommand(t, "zh-CN", "help", "help")
	if !strings.Contains(help, "显示应用中任意命令的帮助") {
		t.Fatalf("localized help command output:\n%s", help)
	}
	completion := runLocalizedCommand(t, "zh-CN", "completion", "--help")
	for _, want := range []string{"为指定 shell 生成 qrypt 的自动补全脚本", "为 bash 生成自动补全脚本"} {
		if !strings.Contains(completion, want) {
			t.Errorf("localized completion help missing %q:\n%s", want, completion)
		}
	}
}

func TestGeneratedHelpFlagsAreLocalized(t *testing.T) {
	chinese := runLocalizedCommand(t, "zh-CN", "fs", "list", "--help")
	if !strings.Contains(chinese, "显示帮助") {
		t.Fatalf("Chinese help flag description missing:\n%s", chinese)
	}
	root := runLocalizedCommand(t, "zh-CN", "--help")
	if !strings.Contains(root, "显示版本信息") {
		t.Fatalf("Chinese version flag description missing:\n%s", root)
	}

	english := runLocalizedCommand(t, "en", "fs", "list", "--help")
	if !strings.Contains(english, "help for list") {
		t.Fatalf("English help flag description changed:\n%s", english)
	}
	if strings.Contains(english, "显示帮助") {
		t.Fatalf("English help leaked Chinese:\n%s", english)
	}
}

func TestEnglishHelpScaffoldingIsUnchanged(t *testing.T) {
	out := runLocalizedCommand(t, "en", "fs", "list", "--help")
	for _, want := range []string{"Usage:", "Examples:", "Flags:", "Global Flags:", "help for list"} {
		if !strings.Contains(out, want) {
			t.Errorf("English help missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"用法:", "选项:", "显示帮助"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("English help contains %q:\n%s", unwanted, out)
		}
	}
}

func TestCommandSchemaMatchesEnglishUnderChineseLocale(t *testing.T) {
	english := NewRootCommand("--lang", "en")
	chinese := NewRootCommand("--lang", "zh-CN")
	if !reflect.DeepEqual(schemaFor(english), schemaFor(chinese)) {
		t.Fatalf("structured command schema differs between English and Chinese locale")
	}
}

func TestErrorsStayEnglishUnderChineseLocale(t *testing.T) {
	root := NewRootCommand("--lang", "zh-CN")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"fs", "list", "--nope"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error for an unknown flag")
	}
	if !strings.Contains(err.Error(), "unknown flag: --nope") {
		t.Fatalf("error was localized or changed: %v", err)
	}
}
