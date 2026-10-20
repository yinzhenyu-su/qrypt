//go:build windows

package i18n

import "testing"

func TestSystemLocaleWindows(t *testing.T) {
	if got := systemLocale(func(string) string { return "" }); got == "" {
		t.Skip("Windows UI locale is unavailable")
	}
}
