package control

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type AuthTestResult struct {
	OpID         string             `json:"op_id"`
	Mount        string             `json:"mount"`
	Driver       string             `json:"driver,omitempty"`
	Pass         bool               `json:"pass"`
	AuthOK       bool               `json:"auth_ok"`
	AuthStatus   string             `json:"auth_status"`
	WriteTested  bool               `json:"write_tested"`
	Capabilities []drive.Capability `json:"capabilities,omitempty"`
	Steps        []AuthTestStep     `json:"steps"`
	RetryCommand string             `json:"retry_command,omitempty"`
	Started      time.Time          `json:"started_at"`
	Finished     time.Time          `json:"finished_at"`
	Duration     string             `json:"duration"`
}

type AuthTestStep struct {
	Operation     string         `json:"operation"`
	Required      bool           `json:"required,omitempty"`
	OK            bool           `json:"ok"`
	Error         string         `json:"error,omitempty"`
	ErrorCategory string         `json:"error_category,omitempty"`
	Duration      string         `json:"duration"`
	Input         map[string]any `json:"input,omitempty"`
	Expected      map[string]any `json:"expected,omitempty"`
	Actual        map[string]any `json:"actual,omitempty"`
}

func RunDriverAuthTest(ctx context.Context, mount string, d drive.Driver) *AuthTestResult {
	result := &AuthTestResult{
		OpID:         newDebugOperationID("auth"),
		Mount:        mount,
		WriteTested:  false,
		AuthStatus:   "unknown",
		Capabilities: drive.Capabilities(d),
		Steps:        make([]AuthTestStep, 0, 5),
		RetryCommand: fmt.Sprintf("qrypt debug test auth --mount %s --socket PATH", mount),
		Started:      time.Now(),
	}
	defer func() {
		result.Finished = time.Now()
		result.Duration = result.Finished.Sub(result.Started).String()
		result.Pass = true
		result.AuthOK = true
		result.AuthStatus = "ok"
		for _, step := range result.Steps {
			if step.Required && !step.OK {
				result.Pass = false
				result.AuthOK = false
				result.AuthStatus = classifyAuthError(step.Error)
				if result.AuthStatus == "" {
					result.AuthStatus = "unknown_error"
				}
				break
			}
		}
	}()

	if debugger, ok := d.(drive.Debugger); ok {
		step := authOptionalStep("debug_snapshot")
		start := time.Now()
		snap, err := debugger.DebugSnapshot(ctx)
		if err == nil {
			result.Driver = snap.Driver
			step.Actual = map[string]any{
				"driver":       snap.Driver,
				"health":       snap.Health,
				"generated_at": snap.GeneratedAt,
				"stats":        snap.Stats,
				"extra":        sanitizedDebugExtra(snap.Extra),
			}
		}
		step.finish(start, err)
		result.Steps = append(result.Steps, step)
	}

	step := authOptionalStep("capability_check")
	step.Expected = map[string]any{"write_tested": false}
	step.Actual = map[string]any{"capabilities": result.Capabilities, "write_tested": false}
	step.finish(time.Now(), nil)
	result.Steps = append(result.Steps, step)

	rootID := "root"
	if resolver, ok := d.(drive.PathResolver); ok {
		step = authRequiredStep("resolve_root")
		step.Input = map[string]any{"path": "/"}
		step.Expected = map[string]any{"root_id": "non-empty"}
		start := time.Now()
		resolved, err := resolver.ResolvePath(ctx, "/")
		if resolved != "" {
			rootID = resolved
		}
		step.Actual = map[string]any{"root_id": resolved}
		if err == nil && resolved == "" {
			err = fmt.Errorf("root resolver returned empty id")
		}
		step.finish(start, err)
		result.Steps = append(result.Steps, step)
		if err != nil {
			return result
		}
	}

	step = authRequiredStep("list_root")
	step.Input = map[string]any{"parent_id": rootID}
	step.Expected = map[string]any{"list_succeeds": true}
	start := time.Now()
	entries, usedRootID, err := listAuthRoot(ctx, d, rootID, !drive.HasCapability(d, drive.CapabilityPathResolver))
	step.Actual = map[string]any{
		"parent_id":   usedRootID,
		"entry_count": len(entries),
		"sample":      authEntrySample(entries, 5),
	}
	step.finish(start, err)
	result.Steps = append(result.Steps, step)
	if err != nil {
		return result
	}

	if space, ok := d.(drive.SpaceQuerier); ok {
		step = authOptionalStep("space_check")
		step.Expected = map[string]any{"space_query": "optional"}
		start = time.Now()
		value, err := space.Space(ctx)
		step.Actual = map[string]any{"total": value.Total, "free": value.Free}
		step.finish(start, err)
		result.Steps = append(result.Steps, step)
	}
	return result
}

func authRequiredStep(operation string) AuthTestStep {
	return AuthTestStep{Operation: operation, Required: true, Duration: "0s"}
}

func authOptionalStep(operation string) AuthTestStep {
	return AuthTestStep{Operation: operation, Duration: "0s"}
}

func (s *AuthTestStep) finish(start time.Time, err error) {
	s.Duration = time.Since(start).String()
	if err != nil {
		s.OK = false
		s.Error = err.Error()
		s.ErrorCategory = drive.ErrorCategory(err)
	} else {
		s.OK = true
	}
}

func authEntrySample(entries []drive.Entry, limit int) []map[string]any {
	if len(entries) == 0 {
		return nil
	}
	if len(entries) < limit {
		limit = len(entries)
	}
	out := make([]map[string]any, 0, limit)
	for _, entry := range entries[:limit] {
		out = append(out, entryActual(entry))
	}
	return out
}

func listAuthRoot(ctx context.Context, d drive.Driver, rootID string, tryCandidates bool) ([]drive.Entry, string, error) {
	if !tryCandidates {
		entries, err := d.List(ctx, rootID)
		return entries, rootID, err
	}
	candidates := []string{rootID, "", "-11", "root", "0"}
	seen := map[string]bool{}
	var lastErr error
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		entries, err := d.List(ctx, candidate)
		if err == nil {
			return entries, candidate, nil
		}
		lastErr = err
	}
	return nil, rootID, lastErr
}

func sanitizedDebugExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]any, len(extra))
	for key, value := range extra {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "cookie") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") {
			out[key] = "***"
			continue
		}
		out[key] = value
	}
	return out
}

func classifyAuthError(message string) string {
	lower := strings.ToLower(message)
	switch {
	case lower == "":
		return ""
	case strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid token") || strings.Contains(lower, "token expired") || strings.Contains(lower, "expired token"):
		return "auth_failed"
	case strings.Contains(lower, "403") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "permission"):
		return "permission_denied"
	case strings.Contains(lower, "captcha") || strings.Contains(lower, "waf") || strings.Contains(lower, "risk") || strings.Contains(lower, "verify"):
		return "blocked_by_provider"
	case strings.Contains(lower, "429") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many request"):
		return "rate_limited"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return "network_timeout"
	case strings.Contains(lower, "root") || strings.Contains(lower, "not found"):
		return "root_unavailable"
	default:
		return "unknown_error"
	}
}
