package logging

import (
	"bytes"
	"strings"
	"testing"
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
