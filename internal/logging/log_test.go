package logging

import (
	"bytes"
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

func TestLoggerStdout(t *testing.T) {
	l, err := New("info", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.writer.(*bytes.Buffer); ok {
		t.Errorf("expected stdout writer")
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
	warnOutput := warnBuf.String()
	if !strings.Contains(warnOutput, "WARN warn one") || strings.Contains(warnOutput, "warn two") || !strings.Contains(warnOutput, "WARN warn three (suppressed=1)") {
		t.Fatalf("unexpected warn sampling output: %s", warnOutput)
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
