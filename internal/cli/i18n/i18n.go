package i18n

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

const (
	LangEN   = "en"
	LangZHCN = "zh-CN"

	annotationKey = "qrypt.i18n.lang"
)

// Localizer renders message catalog entries for one language, falling back to
// English and then to the key itself.
type Localizer struct {
	lang     string
	messages map[string]string
	fallback map[string]string
}

func New(lang string) *Localizer {
	normalized := Normalize(lang)
	if normalized == "" {
		normalized = LangEN
	}
	return &Localizer{
		lang:     normalized,
		messages: catalog[normalized],
		fallback: catalog[LangEN],
	}
}

func (l *Localizer) Lang() string {
	if l == nil {
		return LangEN
	}
	return l.lang
}

func (l *Localizer) T(key string, args ...any) string {
	if l == nil {
		return key
	}
	text := l.messages[key]
	if text == "" {
		text = l.fallback[key]
	}
	if text == "" {
		text = key
	}
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}

// Normalize maps a locale string to a supported language, or returns "" when
// the locale is unsupported.
func Normalize(raw string) string {
	locale := strings.ToLower(strings.TrimSpace(raw))
	if locale == "" {
		return ""
	}
	if idx := strings.IndexAny(locale, ".@"); idx >= 0 {
		locale = locale[:idx]
	}
	locale = strings.ReplaceAll(locale, "_", "-")
	switch {
	case locale == "c", locale == "posix":
		return ""
	case strings.HasPrefix(locale, "zh-hant"), strings.HasPrefix(locale, "zh-tw"), strings.HasPrefix(locale, "zh-hk"):
		return ""
	case locale == "zh", strings.HasPrefix(locale, "zh-"):
		return LangZHCN
	case locale == "en", strings.HasPrefix(locale, "en-"):
		return LangEN
	default:
		return ""
	}
}

// Detect resolves the display language from arguments, the environment, and
// the system locale.
func Detect(args []string, getenv func(string) string) string {
	if value, ok := languageFlag(args); ok && value != "" && value != "auto" {
		return normalizeOrEnglish(value)
	}
	if value := getenv("QRYPT_LANG"); value != "" && value != "auto" {
		return normalizeOrEnglish(value)
	}
	return normalizeOrEnglish(systemLocale(getenv))
}

func normalizeOrEnglish(value string) string {
	if lang := Normalize(value); lang != "" {
		return lang
	}
	return LangEN
}

func languageFlag(args []string) (string, bool) {
	for i, arg := range args {
		if arg == "--lang" && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(arg, "--lang=") {
			return strings.TrimPrefix(arg, "--lang="), true
		}
	}
	return "", false
}

// Attach stores the resolved language on the root command so subcommands can
// localize their human-facing output without changing runtime signatures.
func Attach(root *cobra.Command, lang string) {
	if root == nil {
		return
	}
	if root.Annotations == nil {
		root.Annotations = map[string]string{}
	}
	root.Annotations[annotationKey] = lang
}

// FromCommand returns the localizer attached to the command tree, or English
// when the command is used directly in a test or another host.
func FromCommand(cmd *cobra.Command) *Localizer {
	if cmd != nil {
		if root := cmd.Root(); root != nil {
			if lang := root.Annotations[annotationKey]; lang != "" {
				return New(lang)
			}
		}
	}
	return New(LangEN)
}
