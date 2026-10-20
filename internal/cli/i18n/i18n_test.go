package i18n

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestLocalizeCommandTreeSnapshotsEnglish(t *testing.T) {
	root := &cobra.Command{
		Use:     "qrypt",
		Short:   "Mounts encrypted cloud drives as a local filesystem",
		Long:    "English long description",
		Example: "qrypt mount",
	}
	root.PersistentFlags().String("lang", "auto", "display language: en, zh-CN, or auto")
	Attach(root, LangZHCN)
	LocalizeCommandTree(root)

	if root.Short == "Mounts encrypted cloud drives as a local filesystem" {
		t.Fatalf("root summary was not localized: %q", root.Short)
	}
	if got, ok := EnglishCommandField(root, "short"); !ok || got != "Mounts encrypted cloud drives as a local filesystem" {
		t.Fatalf("English short snapshot = %q, %v", got, ok)
	}
	flag := root.PersistentFlags().Lookup("lang")
	if flag == nil || flag.Usage == "display language: en, zh-CN, or auto" {
		t.Fatalf("lang flag was not localized: %+v", flag)
	}
	if got, ok := EnglishFlagUsage(flag); !ok || got != "display language: en, zh-CN, or auto" {
		t.Fatalf("English flag snapshot = %q, %v", got, ok)
	}
}

func TestNormalize(t *testing.T) {
	tests := map[string]string{
		"zh_CN.UTF-8": LangZHCN,
		"zh-CN":       LangZHCN,
		"zh-Hans":     LangZHCN,
		"zh_SG":       LangZHCN,
		"en_US.UTF-8": LangEN,
		"en-GB":       LangEN,
		"en":          LangEN,
		"C":           "",
		"POSIX":       "",
		"fr_FR.UTF-8": "",
		"zh-TW":       "",
	}
	for input, want := range tests {
		if got := Normalize(input); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDetectPrecedence(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}

	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{
			name: "flag wins",
			args: []string{"--lang", "zh-CN"},
			env:  map[string]string{"QRYPT_LANG": "en", "LANG": "en_US.UTF-8"},
			want: LangZHCN,
		},
		{
			name: "equals form",
			args: []string{"--lang=zh-CN"},
			env:  map[string]string{"LANG": "en_US.UTF-8"},
			want: LangZHCN,
		},
		{
			name: "environment wins over system",
			env:  map[string]string{"QRYPT_LANG": "zh-CN", "LANG": "en_US.UTF-8"},
			want: LangZHCN,
		},
		{
			name: "unix chinese locale",
			env:  map[string]string{"LANG": "zh_CN.UTF-8"},
			want: LangZHCN,
		},
		{
			name: "unix english locale",
			env:  map[string]string{"LANG": "en_US.UTF-8"},
			want: LangEN,
		},
		{
			name: "lc_all wins over lang",
			env:  map[string]string{"LC_ALL": "zh_CN.UTF-8", "LANG": "en_US.UTF-8"},
			want: LangZHCN,
		},
		{
			name: "language list falls through",
			env:  map[string]string{"LANGUAGE": "fr_FR:zh_CN"},
			want: LangZHCN,
		},
		{
			name: "unsupported falls back",
			args: []string{"--lang", "fr"},
			want: LangEN,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Detect(tt.args, env(tt.env)); got != tt.want {
				t.Fatalf("Detect() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocalizerFallback(t *testing.T) {
	original := catalog
	defer func() { catalog = original }()
	catalog = map[string]map[string]string{
		"en":    {"hello": "Hello"},
		"zh-CN": {"hello": "你好"},
	}
	if got := New(LangZHCN).T("hello"); got != "你好" {
		t.Fatalf("T(hello) = %q", got)
	}
	if got := New(LangZHCN).T("missing"); got != "missing" {
		t.Fatalf("T(missing) = %q", got)
	}
	if got := New("fr").Lang(); got != LangEN {
		t.Fatalf("Lang() = %q, want en", got)
	}
}
