package config_test

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
)

// FuzzParseSizeAndDuration pins the size/duration parsers: arbitrary input
// must not panic, and any successful parse must yield a non-negative value
// (durations also never negative).
func FuzzParseSizeAndDuration(f *testing.F) {
	seeds := []string{
		"", "0", "1KB", "10MB", "1.5GB", "100", "abc", "-1", "1 B",
		"1TB", "1K", "1KiB", "1.5", " 2M ", "999999999999999999999GB",
		"5s", "10m", "1h", "-5s", "0.5s", "1d", "1w", "2h30m",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := config.ParseSize(s)
		if err == nil && n < 0 {
			t.Fatalf("ParseSize(%q) = %d, want non-negative", s, n)
		}
		if _, err := config.ParseSize(s); err == nil {
			// Parsing twice must be deterministic.
			m, _ := config.ParseSize(s)
			if m != n {
				t.Fatalf("ParseSize(%q) unstable: %d then %d", s, n, m)
			}
		}
		d, err := config.ParseDuration(s)
		if err == nil && d < 0 {
			t.Fatalf("ParseDuration(%q) = %v, want non-negative", s, d)
		}
		// ParseMaxSize must never panic and never return negative.
		if parsedMax := config.ParseMaxSize(s); parsedMax < 0 {
			t.Fatalf("ParseMaxSize(%q) = %d, want non-negative", s, parsedMax)
		}
	})
}
