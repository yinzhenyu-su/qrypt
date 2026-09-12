package logging

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected Level
	}{
		{"debug", LevelDebug},
		{"info", LevelInfo},
		{"", LevelInfo},
		{"warn", LevelWarn},
		{"warning", LevelWarn},
		{"error", LevelError},
		{"off", LevelOff},
		{"none", LevelOff},
		{"unknown", LevelInfo},
	}

	for _, tt := range tests {
		result := ParseLevel(tt.input)
		if result != tt.expected {
			t.Errorf("ParseLevel(%q) = %d, want %d", tt.input, result, tt.expected)
		}
	}
}

func TestLevelString(t *testing.T) {
	tests := []struct {
		level    Level
		expected string
	}{
		{LevelDebug, "DEBUG"},
		{LevelInfo, "INFO"},
		{LevelWarn, "WARN"},
		{LevelError, "ERROR"},
		{LevelOff, "OFF"},
	}

	for _, tt := range tests {
		if tt.level.String() != tt.expected {
			t.Errorf("Level(%d).String() = %s, want %s", tt.level, tt.level.String(), tt.expected)
		}
	}
}

func TestLoggerDefaultStderr(t *testing.T) {
	l, err := New("info", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.writer != l.errWriter {
		t.Errorf("expected default writer and error writer to share stderr")
	}
}

func TestLoggerLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{level: LevelWarn, writer: &buf}

	l.Debugf("debug msg")
	l.Infof("info msg")
	l.Warnf("warn msg")
	l.Errorf("error msg")

	output := buf.String()
	if strings.Contains(output, "debug msg") {
		t.Error("debug msg should be filtered")
	}
	if strings.Contains(output, "info msg") {
		t.Error("info msg should be filtered")
	}
	if !strings.Contains(output, "WARN warn msg") {
		t.Error("warn msg should appear")
	}
	if !strings.Contains(output, "ERROR error msg") {
		t.Error("error msg should appear")
	}
}

func TestLoggerDebugfEverySamplesRepeatedMessages(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{level: LevelDebug, writer: &buf}

	l.DebugfEvery("hot", 20*time.Millisecond, "first")
	l.DebugfEvery("hot", 20*time.Millisecond, "second")
	time.Sleep(25 * time.Millisecond)
	l.DebugfEvery("hot", 20*time.Millisecond, "third")

	output := buf.String()
	if !strings.Contains(output, "DEBUG first") {
		t.Fatal("first sampled debug message should appear")
	}
	if strings.Contains(output, "DEBUG second") {
		t.Fatal("second sampled debug message should be suppressed")
	}
	if !strings.Contains(output, "DEBUG third (suppressed=1)") {
		t.Fatalf("third message should include suppressed count, got: %s", output)
	}
}

func TestLoggerDebugfEveryFuncBuildsMessageOnlyWhenSampled(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{level: LevelInfo, writer: &buf}
	calls := 0
	l.DebugfEveryFunc("hot", 20*time.Millisecond, func(int) string {
		calls++
		return "hidden"
	})
	if calls != 0 || buf.Len() != 0 {
		t.Fatalf("debug lazy message built at info level: calls=%d output=%q", calls, buf.String())
	}

	l.SetLevel("debug")
	l.DebugfEveryFunc("hot", 20*time.Millisecond, func(int) string {
		calls++
		return "first"
	})
	l.DebugfEveryFunc("hot", 20*time.Millisecond, func(int) string {
		calls++
		return "second"
	})
	time.Sleep(25 * time.Millisecond)
	l.DebugfEveryFunc("hot", 20*time.Millisecond, func(int) string {
		calls++
		return "third"
	})

	output := buf.String()
	if calls != 2 {
		t.Fatalf("lazy message calls = %d, want 2", calls)
	}
	if !strings.Contains(output, "DEBUG first") || strings.Contains(output, "second") || !strings.Contains(output, "DEBUG third (suppressed=1)") {
		t.Fatalf("unexpected lazy sampling output: %s", output)
	}
}

func TestLoggerMainLogContainsEveryLevel(t *testing.T) {
	var mainBuf bytes.Buffer
	var errBuf bytes.Buffer
	l := &Logger{level: LevelDebug, writer: &mainBuf, errWriter: &errBuf}

	l.Debugf("debug msg")
	l.Infof("info msg")
	l.Warnf("warn msg")
	l.Errorf("error msg")

	// The main log must be readable on its own: warn and error lines live
	// there too, not only in the error log.
	mainOutput := mainBuf.String()
	for _, want := range []string{"DEBUG debug msg", "INFO info msg", "WARN warn msg", "ERROR error msg"} {
		if !strings.Contains(mainOutput, want) {
			t.Fatalf("main log missing %q:\n%s", want, mainOutput)
		}
	}

	errOutput := errBuf.String()
	if strings.Contains(errOutput, "debug msg") || strings.Contains(errOutput, "info msg") {
		t.Fatalf("error log should stay a warn+ view:\n%s", errOutput)
	}
	if !strings.Contains(errOutput, "WARN warn msg") || !strings.Contains(errOutput, "ERROR error msg") {
		t.Fatalf("error log missing warn/error lines:\n%s", errOutput)
	}
}

func TestLoggerSharedSinkWritesEachLineOnce(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{level: LevelDebug, writer: &buf, errWriter: &buf}

	l.Infof("info msg")
	l.Warnf("warn msg")
	l.Errorf("error msg")

	if got := strings.Count(buf.String(), "warn msg"); got != 1 {
		t.Fatalf("warn line written %d times to a shared sink, want 1:\n%s", got, buf.String())
	}
	if got := strings.Count(buf.String(), "error msg"); got != 1 {
		t.Fatalf("error line written %d times to a shared sink, want 1:\n%s", got, buf.String())
	}
}

type closeRecorder struct {
	bytes.Buffer
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

// A logger owns the sinks it opened from paths and only those. A writer handed
// in from outside (the default logger's os.Stderr, a test buffer) is borrowed:
// closing it takes down a resource the logger does not own, and for os.Stderr
// that silences the whole process.
func TestReplaceDefaultKeepsBorrowedWriterOpen(t *testing.T) {
	previous := L
	borrowed := &closeRecorder{}
	L = &Logger{level: LevelInfo, writer: borrowed, errWriter: borrowed}
	t.Cleanup(func() { L = previous })

	captured := L
	next, err := New("warn", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ReplaceDefault(next)

	if borrowed.closed {
		t.Fatal("ReplaceDefault closed a writer it did not open")
	}
	if L != captured {
		t.Fatal("global logger identity changed across ReplaceDefault")
	}
	if captured.level != LevelWarn {
		t.Fatalf("captured logger level = %d, want %d", captured.level, LevelWarn)
	}
}

// The mirror image: a sink the previous logger opened itself must still be
// released, or replacing the logger leaks the log file handle. The probe moves
// the log file aside and then writes straight to the captured previous sink (a
// write through the logger would reach the newly installed one instead): a
// released sink reopens, and so recreates, the old path, while an open one keeps
// writing to the inode that now lives under the moved name.
func TestReplaceDefaultClosesPreviousLoggerFileSink(t *testing.T) {
	previous := L
	dir := t.TempDir()
	logPath := filepath.Join(dir, "qrypt.log")
	movedPath := filepath.Join(dir, "qrypt.log.moved")
	oldLogger, err := New("info", logPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	L = oldLogger
	t.Cleanup(func() { L = previous })

	// The lumberjack sink opens its file on the first write.
	L.Infof("first line")
	oldSink := oldLogger.lj
	if oldSink == nil {
		t.Fatal("the file logger has no lumberjack sink")
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("the sink never opened its file: %v", err)
	}
	if err := os.Rename(logPath, movedPath); err != nil {
		t.Skipf("cannot move an open file on this platform: %v", err)
	}

	next, err := New("info", filepath.Join(dir, "next.log"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ReplaceDefault(next)

	if _, err := oldSink.Write([]byte("second line\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("the replaced logger's file sink stayed open: %v", err)
	}
	moved, err := os.ReadFile(movedPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(moved), "second line") {
		t.Fatalf("the replaced logger kept writing to the file it had open:\n%s", moved)
	}
}

// ReplaceDefault's previous writer is os.Stderr for the default logger, and
// *os.File implements io.Closer, so a close-everything cleanup leaves the
// process unable to print: every later write to os.Stderr fails with "file
// already closed". The probe runs in a subprocess because the damage is
// process-wide; in-process it would silence the test binary's own diagnostics
// and every test after it.
func TestReplaceDefaultKeepsProcessStderrUsable(t *testing.T) {
	if os.Getenv("QRYPT_TEST_STDERR_PROBE") == "1" {
		next, err := New("info", filepath.Join(os.Getenv("QRYPT_TEST_STDERR_DIR"), "qrypt.log"), "", nil)
		if err != nil {
			fmt.Fprintln(os.Stdout, "probe: build logger:", err)
			os.Exit(2)
		}
		ReplaceDefault(next)
		if _, err := os.Stderr.WriteString("probe\n"); err != nil {
			fmt.Fprintln(os.Stdout, "probe: stderr unusable after ReplaceDefault:", err)
			os.Exit(3)
		}
		fmt.Fprintln(os.Stdout, "probe: stderr usable")
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestReplaceDefaultKeepsProcessStderrUsable$")
	cmd.Env = append(os.Environ(), "QRYPT_TEST_STDERR_PROBE=1", "QRYPT_TEST_STDERR_DIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stderr probe failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "probe: stderr usable") {
		t.Fatalf("stderr probe did not confirm a usable stderr:\n%s", out)
	}
}

// Close follows the same ownership rule as ReplaceDefault: releasing the
// logger must not release a sink it was given.
func TestCloseKeepsBorrowedWriterOpen(t *testing.T) {
	borrowed := &closeRecorder{}
	l := &Logger{level: LevelInfo, writer: borrowed, errWriter: borrowed}

	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if borrowed.closed {
		t.Fatal("Close closed a writer the logger did not open")
	}
}

func TestLoggerInfofEveryAndWarnfEverySampleAtTheirLevels(t *testing.T) {
	var infoBuf bytes.Buffer
	var warnBuf bytes.Buffer
	l := &Logger{level: LevelInfo, writer: &infoBuf, errWriter: &warnBuf}

	l.InfofEvery("info-hot", 20*time.Millisecond, "info one")
	l.InfofEvery("info-hot", 20*time.Millisecond, "info two")
	l.WarnfEvery("warn-hot", 20*time.Millisecond, "warn one")
	l.WarnfEvery("warn-hot", 20*time.Millisecond, "warn two")
	time.Sleep(25 * time.Millisecond)
	l.InfofEvery("info-hot", 20*time.Millisecond, "info three")
	l.WarnfEvery("warn-hot", 20*time.Millisecond, "warn three")

	infoOutput := infoBuf.String()
	if !strings.Contains(infoOutput, "INFO info one") || strings.Contains(infoOutput, "info two") || !strings.Contains(infoOutput, "INFO info three (suppressed=1)") {
		t.Fatalf("unexpected info sampling output: %s", infoOutput)
	}
	if !strings.Contains(infoOutput, "WARN warn three (suppressed=1)") {
		t.Fatalf("main log missing sampled warn line: %s", infoOutput)
	}
	warnOutput := warnBuf.String()
	if !strings.Contains(warnOutput, "WARN warn one") || strings.Contains(warnOutput, "warn two") || !strings.Contains(warnOutput, "WARN warn three (suppressed=1)") {
		t.Fatalf("unexpected warn sampling output: %s", warnOutput)
	}
	if strings.Contains(warnOutput, "INFO info one") {
		t.Fatalf("error log should not receive info lines: %s", warnOutput)
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ctoken=secret123", "ctoken=***"},
		{"__puus=token456", "__puus=***"},
		{`password = "mypassword"`, `password = "***"`},
		{`password="mypassword"`, `password="***"`},
	}

	for _, tt := range tests {
		result := sanitize(tt.input)
		if result != tt.expected {
			t.Errorf("sanitize(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestSanitizeCredentialParameters(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "quark upload resume id in quoted form",
			input:    `[QUARK] upload resume name="movie.mkv" task="t1" upload_id="resume-secret"`,
			expected: `[QUARK] upload resume name="movie.mkv" task="t1" upload_id="***"`,
		},
		{
			name:     "camel case upload id in bare form",
			input:    `part uploadId=abc123&partNumber=4`,
			expected: `part uploadId=***&partNumber=4`,
		},
		{
			name:     "access token in query string",
			input:    `GET https://api.example.com/v1/files?access_token=secret&limit=10`,
			expected: `GET https://api.example.com/v1/files?access_token=***&limit=10`,
		},
		{
			name:     "refresh token",
			input:    `refresh_token="secret-value" refreshed`,
			expected: `refresh_token="***" refreshed`,
		},
		{
			name:     "generic token parameter",
			input:    `callback token=tok123, retry=1`,
			expected: `callback token=***, retry=1`,
		},
		{
			name:     "authorization header",
			input:    "Authorization: Bearer secret-token\r\nnext line",
			expected: "Authorization: ***\r\nnext line",
		},
		{
			name:     "signed object storage query",
			input:    `upload OSSAccessKeyId=LTAI5t&Signature=abc%2Fdef&Expires=1700`,
			expected: `upload OSSAccessKeyId=***&Signature=***&Expires=1700`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitize(tt.input); got != tt.expected {
				t.Fatalf("sanitize() =\n%q\nwant\n%q", got, tt.expected)
			}
		})
	}
}

func TestSanitizeAppliesToBothSinks(t *testing.T) {
	var mainBuf bytes.Buffer
	var errBuf bytes.Buffer
	l := &Logger{level: LevelInfo, writer: &mainBuf, errWriter: &errBuf}

	l.Warnf("upload resume upload_id=%q", "resume-secret")

	for name, output := range map[string]string{"main log": mainBuf.String(), "error log": errBuf.String()} {
		if strings.Contains(output, "resume-secret") {
			t.Fatalf("%s leaked the upload session id: %s", name, output)
		}
		if !strings.Contains(output, `upload_id="***"`) {
			t.Fatalf("%s missing masked upload id: %s", name, output)
		}
	}
}

func TestSanitizeViaLogger(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{level: LevelInfo, writer: &buf}

	l.Infof("Cookie: ctoken=secret123; __puus=token456")
	output := buf.String()

	if strings.Contains(output, "ctoken=secret123") {
		t.Error("ctoken value should be sanitized")
	}
	if strings.Contains(output, "__puus=token456") {
		t.Error("__puus value should be sanitized")
	}
}

func TestLoggerEventsCaptureWarnAndErrorOnly(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{level: LevelDebug, writer: &buf, errWriter: &buf}

	l.Infof("info msg")
	l.Warnf("warn Cookie: ctoken=secret123")
	l.Errorf("error msg")

	events := l.Events(LevelWarn, 10)
	if len(events) != 2 {
		t.Fatalf("expected two events, got %+v", events)
	}
	if events[0].Level != "WARN" || !strings.Contains(events[0].Message, "Cookie: ***") {
		t.Fatalf("unexpected warn event: %+v", events[0])
	}
	if events[1].Level != "ERROR" || events[1].Message != "error msg" {
		t.Fatalf("unexpected error event: %+v", events[1])
	}
}

func TestLoggerEventsLimitAndLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{level: LevelDebug, writer: &buf, errWriter: &buf}

	l.Warnf("warn one")
	l.Errorf("error one")
	l.Errorf("error two")

	events := l.Events(LevelError, 1)
	if len(events) != 1 {
		t.Fatalf("expected one event, got %+v", events)
	}
	if events[0].Message != "error two" {
		t.Fatalf("expected latest error, got %+v", events[0])
	}
}

func TestLoggerSampledEventsIncludeSuppressedCount(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{level: LevelDebug, writer: &buf, errWriter: &buf}

	l.WarnfEvery("hot-event", 20*time.Millisecond, "first")
	l.WarnfEvery("hot-event", 20*time.Millisecond, "second")
	time.Sleep(25 * time.Millisecond)
	l.WarnfEvery("hot-event", 20*time.Millisecond, "third")

	events := l.Events(LevelWarn, 10)
	if len(events) != 2 {
		t.Fatalf("expected two sampled events, got %+v", events)
	}
	if events[1].Suppressed != 1 || !strings.Contains(events[1].Message, "suppressed=1") {
		t.Fatalf("expected suppressed count on second emitted event, got %+v", events[1])
	}
}
