package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
)

func TestCommandTextCatalogCoverage(t *testing.T) {
	root := NewRootCommand()
	en := i18n.New(i18n.LangEN)
	zh := i18n.New(i18n.LangZHCN)

	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
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
