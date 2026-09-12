package logging

import (
	"testing"
)

// The log functions format and sanitize the message before writing it, so
// every emitted line pays the redaction scan. These benchmarks quantify that
// cost.

func BenchmarkSanitizeTypicalLine(b *testing.B) {
	line := `[VFS] upload start op_id="fid-1" path="/movies/a.mkv" size=1048576 local="/tmp/x.staging"`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sanitize(line)
	}
}

func BenchmarkSanitizeCredentialLine(b *testing.B) {
	line := `[QUARK] upload resume name="a.mkv" task="t1" upload_id="abc123" part=4`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sanitize(line)
	}
}

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
