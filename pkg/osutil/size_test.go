package osutil

import "testing"

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{KiB, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{MiB, "1.00 MiB"},
		{5 * GiB, "5.00 GiB"},
		{2 * TiB, "2.00 TiB"},
	}
	for _, tt := range tests {
		if got := FormatBytes(tt.bytes); got != tt.want {
			t.Fatalf("FormatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		value int64
		unit  int64
		want  string
	}{
		{40960, MiB, "40.00 GiB"},
		{40, GiB, "40.00 GiB"},
		{5, Byte, "5 B"},
		{1, 0, "1 B"},
	}
	for _, tt := range tests {
		if got := FormatSize(tt.value, tt.unit); got != tt.want {
			t.Fatalf("FormatSize(%d, %d) = %q, want %q", tt.value, tt.unit, got, tt.want)
		}
	}
}
