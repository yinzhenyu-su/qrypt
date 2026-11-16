//go:build !windows

package i18n

import "testing"

// TestDetectUnixLocalePrecedence covers the Unix locale environment variables.
// On Windows the system locale comes from the Win32 API and ignores these
// variables, so the cases live here instead of in the platform-neutral test.
func TestDetectUnixLocalePrecedence(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Detect(nil, env(tt.env)); got != tt.want {
				t.Fatalf("Detect() = %q, want %q", got, tt.want)
			}
		})
	}
}
