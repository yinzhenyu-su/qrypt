package logging

import (
	"testing"
)

// The log functions format and sanitize (9 regex passes) the message before
// checking the configured level, so disabled-level calls still pay for string
// building. These benchmarks quantify that cost.

func BenchmarkDebugfEveryWhenDisabled(b *testing.B) {
	l := &Logger{level: LevelInfo, writer: discardWriter{}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.DebugfEvery("bench.key", 0, "[FUSE] %s path=%q errc=%d took=%v", "Read", "/a/b.txt", 0, 12345)
	}
}

func BenchmarkInfofEveryWhenEnabled(b *testing.B) {
	l := &Logger{level: LevelInfo, writer: discardWriter{}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.InfofEvery("bench.key", 0, "[VFS] upload start op_id=%q path=%q size=%d", "fid", "/a/b.txt", 1024)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
