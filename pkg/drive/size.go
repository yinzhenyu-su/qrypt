package drive

import "fmt"

const (
	Byte int64 = 1
	KiB        = 1 << 10
	MiB        = 1 << 20
	GiB        = 1 << 30
	TiB        = 1 << 40
	PiB        = 1 << 50
	EiB        = 1 << 60
)

// FormatSize formats a size value whose input unit is explicitly specified.
func FormatSize(value, unit int64) string {
	if unit <= 0 {
		unit = Byte
	}
	return FormatBytes(value * unit)
}

// FormatBytes formats a byte count using binary units.
func FormatBytes(b int64) string {
	const unit = KiB
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < len("KMGTPE")-1; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
