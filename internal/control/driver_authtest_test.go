package control

import (
	"context"
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestRunDriverAuthTestChecksRootWithoutWrites(t *testing.T) {
	driver := newCRUDMemoryDriver()
	result := RunDriverAuthTest(context.Background(), "mem", driver)
	if !result.Pass || !result.AuthOK || result.AuthStatus != "ok" || result.WriteTested {
		t.Fatalf("unexpected auth result: %+v", result)
	}
	if result.OpID == "" || result.RetryCommand == "" || len(result.Capabilities) == 0 {
		t.Fatalf("auth result missing metadata: %+v", result)
	}
	seen := map[string]bool{}
	for _, step := range result.Steps {
		seen[step.Operation] = true
	}
	for _, op := range []string{"debug_snapshot", "capability_check", "resolve_root", "list_root"} {
		if !seen[op] {
			t.Fatalf("missing auth step %q in %+v", op, result.Steps)
		}
	}
}

func TestRunDriverAuthTestClassifiesAuthFailure(t *testing.T) {
	driver := newCRUDMemoryDriver()
	driver.listErr = errors.New("401 Unauthorized")
	result := RunDriverAuthTest(context.Background(), "mem", driver)
	if result.Pass || result.AuthOK || result.AuthStatus != "auth_failed" {
		t.Fatalf("unexpected auth failure result: %+v", result)
	}
}

func TestClassifyAuthError(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want string
	}{
		{"401 Unauthorized", "auth_failed"},
		{"403 forbidden", "permission_denied"},
		{"captcha required", "blocked_by_provider"},
		{"429 too many requests", "rate_limited"},
		{"context deadline exceeded", "network_timeout"},
		{"root not found", "root_unavailable"},
		{"weird", "unknown_error"},
	} {
		if got := classifyAuthError(tc.msg); got != tc.want {
			t.Fatalf("classifyAuthError(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

var _ drive.Driver = (*crudMemoryDriver)(nil)
