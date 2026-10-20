//go:build !windows

package i18n

import "strings"

func systemLocale(getenv func(string) string) string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		value := strings.TrimSpace(getenv(key))
		if value != "" && Normalize(value) != "" {
			return value
		}
	}
	for _, value := range strings.Split(getenv("LANGUAGE"), ":") {
		value = strings.TrimSpace(value)
		if value != "" && Normalize(value) != "" {
			return value
		}
	}
	return ""
}
