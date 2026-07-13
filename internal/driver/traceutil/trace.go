package traceutil

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type Buffer struct {
	mu     sync.Mutex
	events []drive.DebugTraceEvent
	limit  int
}

func NewBuffer(limit int) *Buffer {
	if limit <= 0 {
		limit = 500
	}
	return &Buffer{limit: limit}
}

func (b *Buffer) Record(ctx context.Context, event drive.DebugTraceEvent) {
	if b == nil {
		return
	}
	if op, ok := drive.DebugOperationFromContext(ctx); ok {
		event.OpID = op.OpID
		event.Step = op.Step
		event.Name = op.Name
	}
	if event.At.IsZero() {
		event.At = time.Now()
	}
	if event.Layer == "" {
		event.Layer = "driver.http"
	}
	event.SensitiveMasked = true
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	if len(b.events) > b.limit {
		b.events = append([]drive.DebugTraceEvent(nil), b.events[len(b.events)-b.limit:]...)
	}
}

func (b *Buffer) Events(since time.Time) []drive.DebugTraceEvent {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]drive.DebugTraceEvent, 0, len(b.events))
	for _, event := range b.events {
		if event.At.Before(since) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func URL(u *url.URL) string {
	if u == nil {
		return ""
	}
	out := *u
	out.RawQuery = ""
	out.ForceQuery = false
	out.RawFragment = ""
	out.Fragment = ""
	out.User = nil
	return out.String()
}

func RequestFields(fields map[string]string) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return map[string]any{"fields": keys}
}

func BodyFields(body any) map[string]any {
	fields := bodyFields(body)
	if len(fields) == 0 {
		return nil
	}
	sort.Strings(fields)
	return map[string]any{"fields": fields}
}

func bodyFields(body any) []string {
	switch v := body.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		return keys
	case map[string]string:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		return keys
	default:
		return nil
	}
}

func MergeRequest(parts ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, part := range parts {
		for key, value := range part {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func HeaderKeys(headers http.Header) []string {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var sensitiveSnippetPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(access_token=)[^&\s"']+`),
	regexp.MustCompile(`(?i)(refresh_token=)[^&\s"']+`),
	regexp.MustCompile(`(?i)(sessionKey=)[^,\s"']+`),
	regexp.MustCompile(`(?i)("access_token"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("refresh_token"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("sessionKey"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("downloadUrl"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("fileDownloadUrl"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("requestURL"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)(Cookie:\s*)[^,\s"']+`),
	regexp.MustCompile(`(?i)(Authorization:\s*)[^,\s"']+`),
	regexp.MustCompile(`(?i)(token=)[^&\s"']+`),
}

func Snippet(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 300 {
		raw = raw[:300]
	}
	snippet := string(raw)
	for _, pattern := range sensitiveSnippetPatterns {
		snippet = pattern.ReplaceAllString(snippet, "${1}<masked>")
	}
	return snippet
}
