package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

var sensitivePatterns = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{regexp.MustCompile(`ctoken=[^;]+`),        "ctoken=***"},
	{regexp.MustCompile(`__puus=[^;]+`),         "__puus=***"},
	{regexp.MustCompile(`__kp=[^;]+`),           "__kp=***"},
	{regexp.MustCompile(`__kps=[^;]+`),          "__kps=***"},
	{regexp.MustCompile(`password="[^"]+"`), `password="***"`},
	{regexp.MustCompile(`password = "[^"]+"`), `password = "***"`},
	{regexp.MustCompile(`salt\s*=\s*"[^"]*"`),     `salt=""`},
	{regexp.MustCompile(`"cookie"\s*:\s*"[^"]+"`), `"cookie":"***"`},
	{regexp.MustCompile(`Cookie:\s*[^\r\n]+`),     "Cookie: ***"},
}

func sanitize(msg string) string {
	for _, sp := range sensitivePatterns {
		msg = sp.pattern.ReplaceAllString(msg, sp.replace)
	}
	return msg
}

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelOff
)

func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "info", "":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	case "off", "none":
		return LevelOff
	default:
		return LevelInfo
	}
}

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	case LevelOff:
		return "OFF"
	default:
		return "UNKNOWN"
	}
}

type RotateConfig struct {
	MaxSize    int
	MaxBackups int
	MaxAge     int
	Compress   bool
}

var DefaultRotateConfig = RotateConfig{
	MaxSize:    100,
	MaxBackups: 7,
	MaxAge:     28,
	Compress:   true,
}

type Logger struct {
	level      Level
	writer     io.Writer
	errWriter  io.Writer
	lj         *lumberjack.Logger
	errLj      *lumberjack.Logger
	mu         sync.Mutex
}

func errorLogPath(logFile string) string {
	if logFile == "" {
		return ""
	}
	ext := filepath.Ext(logFile)
	base := strings.TrimSuffix(logFile, ext)
	return base + "-err" + ext
}

func openLumberjack(path string, rc RotateConfig) *lumberjack.Logger {
	return &lumberjack.Logger{
		Filename:   path,
		MaxSize:    rc.MaxSize,
		MaxBackups: rc.MaxBackups,
		MaxAge:     rc.MaxAge,
		Compress:   rc.Compress,
	}
}

func New(level string, logFile string, errorFile string, rotate *RotateConfig) (*Logger, error) {
	rc := DefaultRotateConfig
	if rotate != nil {
		if rotate.MaxSize > 0 {
			rc.MaxSize = rotate.MaxSize
		}
		if rotate.MaxBackups > 0 {
			rc.MaxBackups = rotate.MaxBackups
		}
		if rotate.MaxAge > 0 {
			rc.MaxAge = rotate.MaxAge
		}
		rc.Compress = rotate.Compress
	}

	l := &Logger{level: ParseLevel(level)}

	if logFile != "" {
		l.writer = openLumberjack(logFile, rc)
		l.lj = l.writer.(*lumberjack.Logger)

		if errorFile == "" {
			errorFile = errorLogPath(logFile)
		}
		if errorFile != logFile {
			l.errWriter = openLumberjack(errorFile, rc)
			l.errLj = l.errWriter.(*lumberjack.Logger)
		} else {
			l.errWriter = l.writer
		}
	} else {
		l.writer = os.Stdout
		l.errWriter = os.Stderr
	}

	return l, nil
}

func (l *Logger) SetLevel(level string) {
	l.mu.Lock()
	l.level = ParseLevel(level)
	l.mu.Unlock()
}

func (l *Logger) logf(level Level, format string, v ...interface{}) {
	if level < l.level {
		return
	}
	msg := sanitize(fmt.Sprintf(format, v...))
	ts := time.Now().Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("[%s] %s %s\n", ts, level.String(), msg)

	l.mu.Lock()
	if level >= LevelWarn && l.errWriter != nil {
		fmt.Fprint(l.errWriter, line)
	} else if l.writer != nil {
		fmt.Fprint(l.writer, line)
	}
	l.mu.Unlock()
}

func (l *Logger) Debugf(format string, v ...interface{}) { l.logf(LevelDebug, format, v...) }
func (l *Logger) Infof(format string, v ...interface{})  { l.logf(LevelInfo, format, v...) }
func (l *Logger) Warnf(format string, v ...interface{})  { l.logf(LevelWarn, format, v...) }
func (l *Logger) Errorf(format string, v ...interface{}) { l.logf(LevelError, format, v...) }

func (l *Logger) Debug(args ...interface{}) { l.logf(LevelDebug, "%s", fmt.Sprint(args...)) }
func (l *Logger) Info(args ...interface{})  { l.logf(LevelInfo, "%s", fmt.Sprint(args...)) }
func (l *Logger) Warn(args ...interface{})  { l.logf(LevelWarn, "%s", fmt.Sprint(args...)) }
func (l *Logger) Error(args ...interface{}) { l.logf(LevelError, "%s", fmt.Sprint(args...)) }

func (l *Logger) Rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lj != nil {
		if err := l.lj.Rotate(); err != nil {
			return err
		}
	}
	if l.errLj != nil && l.errLj != l.lj {
		return l.errLj.Rotate()
	}
	return nil
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lj != nil {
		if err := l.lj.Close(); err != nil {
			return err
		}
	}
	if l.errLj != nil && l.errLj != l.lj {
		return l.errLj.Close()
	}
	if c, ok := l.writer.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

var L = NewDefault()

func NewDefault() *Logger {
	l, err := New("info", "", "", nil)
	if err != nil {
		panic(err)
	}
	return l
}
