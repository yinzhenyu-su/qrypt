package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/util"
	"gopkg.in/natefinch/lumberjack.v2"
)

// logTimeFormat renders a local-time timestamp with a numeric UTC offset
// (e.g. 2006-01-02 15:04:05.000 +08:00), so the zone is explicit.
const logTimeFormat = "2006-01-02 15:04:05.000 -07:00"

type redaction struct {
	pattern *regexp.Regexp
	replace string
}

// credentialParams are parameter names whose values are masked wherever they
// appear in a log line: query strings, headers and formatted key/value pairs
// all use the name=value or name="value" shape. Log files travel in
// `qrypt debug bundle`, so a leaked credential here reaches whoever reads the
// report.
var credentialParams = []string{
	"upload_id", "uploadId",
	"access_token", "refresh_token", "token",
	"OSSAccessKeyId", "Signature",
}

var sensitivePatterns = buildSensitivePatterns()

func buildSensitivePatterns() []redaction {
	patterns := []redaction{
		{regexp.MustCompile(`ctoken=[^;]+`), "ctoken=***"},
		{regexp.MustCompile(`__puus=[^;]+`), "__puus=***"},
		{regexp.MustCompile(`__kp=[^;]+`), "__kp=***"},
		{regexp.MustCompile(`__kps=[^;]+`), "__kps=***"},
		{regexp.MustCompile(`password="[^"]+"`), `password="***"`},
		{regexp.MustCompile(`password = "[^"]+"`), `password = "***"`},
		{regexp.MustCompile(`salt\s*=\s*"[^"]*"`), `salt=""`},
		{regexp.MustCompile(`"cookie"\s*:\s*"[^"]+"`), `"cookie":"***"`},
		{regexp.MustCompile(`Cookie:\s*[^\r\n]+`), "Cookie: ***"},
		{regexp.MustCompile(`Authorization:\s*[^\r\n]+`), "Authorization: ***"},
	}
	// One alternation per value shape rather than one pattern per name: every
	// log line pays this scan, so the number of regex passes matters.
	names := strings.Join(credentialParams, "|")
	patterns = append(patterns,
		redaction{regexp.MustCompile(`\b(` + names + `)="[^"]*"`), `${1}="***"`},
		redaction{regexp.MustCompile(`\b(` + names + `)=[^\s,;&"']+`), `${1}=***`},
	)
	return patterns
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
	level     Level
	writer    io.Writer
	errWriter io.Writer
	lj        *lumberjack.Logger
	errLj     *lumberjack.Logger
	mu        sync.Mutex
	samples   map[string]sampleState
	events    eventRing
}

type sampleState struct {
	last       time.Time
	suppressed int
}

// Event is one retained log line. Mount and Component are fields so the query
// surface can filter on them instead of scanning Message: Component is derived
// from the leading "[TAG]" of the message at write time, and Mount is supplied
// by the calling Scope (empty for call sites that are not mount-scoped).
type Event struct {
	ID         uint64    `json:"id"`
	Time       time.Time `json:"time"`
	Level      string    `json:"level"`
	Message    string    `json:"message"`
	Mount      string    `json:"mount,omitempty"`
	Component  string    `json:"component,omitempty"`
	Suppressed int       `json:"suppressed,omitempty"`
}

// componentOf returns the leading "[TAG]" of a message with the brackets
// stripped and the tag upper-cased, matching what readers of the log text see.
// Messages without that prefix have no component.
func componentOf(msg string) string {
	if !strings.HasPrefix(msg, "[") {
		return ""
	}
	end := strings.Index(msg, "]")
	if end <= 1 {
		return ""
	}
	return strings.ToUpper(msg[1:end])
}

// sampleKey namespaces a call site's sampling key by mount so two mounts
// running the same code path keep independent suppression state.
func sampleKey(mount, key string) string {
	if mount == "" {
		return key
	}
	return mount + "\x00" + key
}

// mountPrefix renders a scope's mount as a leading field, so the log text
// itself is attributable (and greppable) rather than only the event ring.
func mountPrefix(mount string) string {
	if mount == "" {
		return ""
	}
	return "mount=" + strconv.Quote(mount) + " "
}

type eventRing struct {
	next  uint64
	items []Event
}

const defaultEventLimit = 500

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
		l.writer = os.Stderr
		l.errWriter = os.Stderr
	}

	return l, nil
}

func (l *Logger) SetLevel(level string) {
	l.mu.Lock()
	l.level = ParseLevel(level)
	l.mu.Unlock()
}

// Enabled reports whether messages at level would be logged. Callers on hot
// paths should check it before building expensive message strings, since the
// log functions format and sanitize the message before applying the level
// filter.
func (l *Logger) Enabled(level Level) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return level >= l.level
}

// writeLineLocked emits one rendered line. Every line goes to the main log so
// that it can be read on its own in time order; warn and above are additionally
// written to the error log, which is a filtered view rather than the only place
// errors appear. A shared sink (no file output) is written once.
func (l *Logger) writeLineLocked(level Level, line string) {
	if l.writer != nil {
		fmt.Fprint(l.writer, line)
	}
	if level >= LevelWarn && l.errWriter != nil && l.errWriter != l.writer {
		fmt.Fprint(l.errWriter, line)
	}
}

func (l *Logger) logf(level Level, mount string, format string, v ...interface{}) {
	// Check the level before building/sanitizing the message: the format and
	// sanitize passes are expensive (regex) and were paid even when the level
	// is filtered.
	if !l.Enabled(level) {
		return
	}
	msg := sanitize(fmt.Sprintf(format, v...))
	now := util.Now()
	ts := now.Format(logTimeFormat)
	line := fmt.Sprintf("[%s] %s %s%s\n", ts, level.String(), mountPrefix(mount), msg)

	l.mu.Lock()
	if level < l.level {
		l.mu.Unlock()
		return
	}
	l.recordEventLocked(level, mount, now, msg, 0)
	l.writeLineLocked(level, line)
	l.mu.Unlock()
}

func (l *Logger) logfEvery(level Level, mount, key string, interval time.Duration, format string, v ...interface{}) {
	if !l.Enabled(level) {
		return
	}
	if interval <= 0 {
		l.logf(level, mount, format, v...)
		return
	}
	sampleNow := time.Now()
	now := util.Now()
	msg := sanitize(fmt.Sprintf(format, v...))
	ts := now.Format(logTimeFormat)
	key = sampleKey(mount, key)

	l.mu.Lock()
	if level < l.level {
		l.mu.Unlock()
		return
	}
	if l.samples == nil {
		l.samples = map[string]sampleState{}
	}
	state := l.samples[key]
	if state.last.IsZero() || sampleNow.Sub(state.last) >= interval {
		suppressed := state.suppressed
		if state.suppressed > 0 {
			msg = fmt.Sprintf("%s (suppressed=%d)", msg, state.suppressed)
		}
		l.samples[key] = sampleState{last: sampleNow}
		l.recordEventLocked(level, mount, now, msg, suppressed)
		line := fmt.Sprintf("[%s] %s %s%s\n", ts, level.String(), mountPrefix(mount), msg)
		l.writeLineLocked(level, line)
		l.mu.Unlock()
		return
	}
	state.suppressed++
	l.samples[key] = state
	l.mu.Unlock()
}

func (l *Logger) logEveryFunc(level Level, mount, key string, interval time.Duration, message func(suppressed int) string) {
	if interval <= 0 {
		l.logf(level, mount, "%s", message(0))
		return
	}
	sampleNow := time.Now()
	now := util.Now()
	key = sampleKey(mount, key)

	l.mu.Lock()
	if level < l.level {
		l.mu.Unlock()
		return
	}
	if l.samples == nil {
		l.samples = map[string]sampleState{}
	}
	state := l.samples[key]
	if !state.last.IsZero() && sampleNow.Sub(state.last) < interval {
		state.suppressed++
		l.samples[key] = state
		l.mu.Unlock()
		return
	}
	suppressed := state.suppressed
	l.samples[key] = sampleState{last: sampleNow}
	l.mu.Unlock()

	msg := sanitize(message(suppressed))
	if suppressed > 0 {
		msg = fmt.Sprintf("%s (suppressed=%d)", msg, suppressed)
	}
	ts := now.Format(logTimeFormat)
	line := fmt.Sprintf("[%s] %s %s%s\n", ts, level.String(), mountPrefix(mount), msg)

	l.mu.Lock()
	l.recordEventLocked(level, mount, now, msg, suppressed)
	l.writeLineLocked(level, line)
	l.mu.Unlock()
}

func (l *Logger) Debugf(format string, v ...interface{}) { l.logf(LevelDebug, "", format, v...) }
func (l *Logger) Infof(format string, v ...interface{})  { l.logf(LevelInfo, "", format, v...) }
func (l *Logger) Warnf(format string, v ...interface{})  { l.logf(LevelWarn, "", format, v...) }
func (l *Logger) Errorf(format string, v ...interface{}) { l.logf(LevelError, "", format, v...) }

func (l *Logger) DebugfEvery(key string, interval time.Duration, format string, v ...interface{}) {
	l.logfEvery(LevelDebug, "", key, interval, format, v...)
}

func (l *Logger) DebugfEveryFunc(key string, interval time.Duration, message func(suppressed int) string) {
	l.logEveryFunc(LevelDebug, "", key, interval, message)
}

func (l *Logger) InfofEvery(key string, interval time.Duration, format string, v ...interface{}) {
	l.logfEvery(LevelInfo, "", key, interval, format, v...)
}

func (l *Logger) WarnfEvery(key string, interval time.Duration, format string, v ...interface{}) {
	l.logfEvery(LevelWarn, "", key, interval, format, v...)
}

func (l *Logger) Debug(args ...interface{}) { l.logf(LevelDebug, "", "%s", fmt.Sprint(args...)) }
func (l *Logger) Info(args ...interface{})  { l.logf(LevelInfo, "", "%s", fmt.Sprint(args...)) }
func (l *Logger) Warn(args ...interface{})  { l.logf(LevelWarn, "", "%s", fmt.Sprint(args...)) }
func (l *Logger) Error(args ...interface{}) { l.logf(LevelError, "", "%s", fmt.Sprint(args...)) }

// Scope is a mount-stamped view of a logger. It holds the *Logger rather than
// a copy of its fields, so it keeps writing to whatever the logger points at
// after ReplaceDefault mutates the global in place.
type Scope struct {
	logger *Logger
	mount  string
}

// WithMount returns a scope that stamps every line it writes with the given
// mount. Call sites that are not mount-scoped keep using the logger directly.
func (l *Logger) WithMount(mount string) *Scope {
	return &Scope{logger: l, mount: mount}
}

// Mount returns the mount this scope stamps.
func (s *Scope) Mount() string {
	if s == nil {
		return ""
	}
	return s.mount
}

// A nil scope drops its line instead of panicking: a zero-value owner (a
// struct built without its constructor in a test, say) must never take down
// the data path it is trying to instrument.

func (s *Scope) Debugf(format string, v ...interface{}) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.logf(LevelDebug, s.mount, format, v...)
}
func (s *Scope) Infof(format string, v ...interface{}) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.logf(LevelInfo, s.mount, format, v...)
}
func (s *Scope) Warnf(format string, v ...interface{}) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.logf(LevelWarn, s.mount, format, v...)
}
func (s *Scope) Errorf(format string, v ...interface{}) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.logf(LevelError, s.mount, format, v...)
}

func (s *Scope) DebugfEvery(key string, interval time.Duration, format string, v ...interface{}) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.logfEvery(LevelDebug, s.mount, key, interval, format, v...)
}

func (s *Scope) InfofEvery(key string, interval time.Duration, format string, v ...interface{}) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.logfEvery(LevelInfo, s.mount, key, interval, format, v...)
}

func (s *Scope) WarnfEvery(key string, interval time.Duration, format string, v ...interface{}) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.logfEvery(LevelWarn, s.mount, key, interval, format, v...)
}

// DebugfEveryFunc builds its message lazily, so suppressed calls pay nothing.
func (s *Scope) DebugfEveryFunc(key string, interval time.Duration, message func(suppressed int) string) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.logEveryFunc(LevelDebug, s.mount, key, interval, message)
}

func (l *Logger) Events(minLevel Level, limit int) []Event {
	if limit <= 0 || limit > defaultEventLimit {
		limit = defaultEventLimit
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Event
	for i := len(l.events.items) - 1; i >= 0 && len(out) < limit; i-- {
		event := l.events.items[i]
		if ParseLevel(event.Level) < minLevel {
			continue
		}
		out = append(out, event)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (l *Logger) recordEventLocked(level Level, mount string, ts time.Time, msg string, suppressed int) {
	if level < LevelWarn || level >= LevelOff {
		return
	}
	l.events.next++
	event := Event{
		ID:         l.events.next,
		Time:       ts,
		Level:      level.String(),
		Message:    msg,
		Mount:      mount,
		Component:  componentOf(msg),
		Suppressed: suppressed,
	}
	if len(l.events.items) < defaultEventLimit {
		l.events.items = append(l.events.items, event)
		return
	}
	copy(l.events.items, l.events.items[1:])
	l.events.items[len(l.events.items)-1] = event
}

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

func ReplaceDefault(next *Logger) {
	if next == nil {
		return
	}
	L.mu.Lock()
	oldLJ := L.lj
	oldErrLJ := L.errLj
	oldWriter := L.writer
	L.level = next.level
	L.writer = next.writer
	L.errWriter = next.errWriter
	L.lj = next.lj
	L.errLj = next.errLj
	L.samples = nil
	L.events = eventRing{}
	L.mu.Unlock()
	if oldLJ != nil {
		_ = oldLJ.Close()
	}
	if oldErrLJ != nil && oldErrLJ != oldLJ {
		_ = oldErrLJ.Close()
	}
	if c, ok := oldWriter.(io.Closer); ok && c != oldLJ && c != oldErrLJ {
		_ = c.Close()
	}
}

func NewDefault() *Logger {
	l, err := New("info", "", "", nil)
	if err != nil {
		panic(err)
	}
	return l
}
