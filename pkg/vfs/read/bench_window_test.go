package read

import (
	"fmt"
	"testing"
)

func BenchmarkFindWindow(b *testing.B) {
	for _, count := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("windows-%d", count), func(b *testing.B) {
			state := NewState(nil)
			for i := 0; i < count; i++ {
				start := int64(i * 8)
				key := WindowKey("fid", start, start+7)
				state.windows.loads[key] = &windowLoad{
					fid:     "fid",
					start:   start,
					end:     start + 7,
					streams: 1,
					done:    make(chan struct{}),
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				index := int64((i%count)*8 + 3)
				if state.findWindow("fid", index, 1) == nil {
					b.Fatal("window not found")
				}
			}
		})
	}
}
