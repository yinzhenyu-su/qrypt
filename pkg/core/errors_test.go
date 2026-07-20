package core

import (
	"context"
	"fmt"
	"testing"
)

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCode  ErrorCode
		retryable bool
	}{
		{name: "timeout", err: context.DeadlineExceeded, wantCode: ErrorCodeNetworkRetryable, retryable: true},
		{name: "auth", err: fmt.Errorf("quark: 401 unauthorized"), wantCode: ErrorCodeAuthExpired},
		{name: "permission", err: fmt.Errorf("quark: 403 forbidden"), wantCode: ErrorCodePermission},
		{name: "not found", err: fmt.Errorf("vfs: not found: /x"), wantCode: ErrorCodeNotFound},
		{name: "rate limit", err: fmt.Errorf("429 too many requests"), wantCode: ErrorCodeRateLimited, retryable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err)
			if got.Code != tt.wantCode || got.Retryable != tt.retryable {
				t.Fatalf("ClassifyError = %+v, want code=%s retryable=%t", got, tt.wantCode, tt.retryable)
			}
		})
	}
}
