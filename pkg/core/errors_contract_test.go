package core

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestClassifyErrorWrappedPreservesCategory: the taxonomy must survive any
// number of %w wraps - a wrapped driver error classifies exactly like the
// bare error. This is the contract the CLI/control layers rely on when
// they classify errors that travelled through VFS/core frames.
func TestClassifyErrorWrappedPreservesCategory(t *testing.T) {
	base := fmt.Errorf("quark: 403 forbidden")
	wrapped := fmt.Errorf("vfs: upload failed: %w", fmt.Errorf("retry 1 of 3: %w", base))
	a := ClassifyError(base)
	b := ClassifyError(wrapped)
	if a.Category != b.Category || a.Code != b.Code || a.Retryable != b.Retryable {
		t.Fatalf("wrapping changed classification: bare=%+v wrapped=%+v", a, b)
	}
}

// TestClassifyContextCanceledIsNotNetwork: cancellation is its own class
// and must never fall into the network bucket, or retry logic would treat
// a user interrupt as a transient provider failure.
func TestClassifyContextCanceledIsNotNetwork(t *testing.T) {
	for _, err := range []error{
		context.Canceled,
		fmt.Errorf("upload interrupted: %w", context.Canceled),
		fmt.Errorf("quark: %w", context.Canceled),
	} {
		info := ClassifyError(err)
		if info.Category == drive.ErrorCategoryNetwork || info.Category == drive.ErrorCategoryTimeout {
			t.Fatalf("context cancellation misclassified as network: %+v", info)
		}
		if info.Category != drive.ErrorCategoryCancelled {
			t.Fatalf("context cancellation = %q, want %q", info.Category, drive.ErrorCategoryCancelled)
		}
		if info.Retryable {
			t.Fatalf("context cancellation must not be retryable: %+v", info)
		}
	}
}

// TestClassifyProviderErrorsMapStably: real provider error strings must map
// to the intended buckets and never degrade to unknown as they flow
// through the taxonomy.
func TestClassifyProviderErrorsMapStably(t *testing.T) {
	tests := []struct {
		message string
		want    ErrorCode
	}{
		{"p115: Init: missing cookie", ErrorCodeAuthExpired},
		{"115: authorization has expired", ErrorCodeAuthExpired},
		{"token refresh failed: invalid token", ErrorCodeAuthExpired},
		{"quark: 403 access denied", ErrorCodePermission},
		{"aliyundrive: no space left", ErrorCodeLocalIO},
		{"yun139: 429 too many requests", ErrorCodeRateLimited},
		{"p115: resolve root_path: not found", ErrorCodeNotFound},
		{"quark: 502 bad gateway", ErrorCodeNetworkRetryable},
		{"vfs: pending journal write failed: staging write", ErrorCodePersistence},
		{"max retries exceeded", ErrorCodeUnknown},
	}
	for _, tt := range tests {
		info := ClassifyErrorMessage(tt.message)
		if info.Code != tt.want {
			t.Errorf("ClassifyErrorMessage(%q) = %+v, want code %s", tt.message, info, tt.want)
		}
	}
}

// TestRetryableAndPermanentDecisionsConsistent: the retryable flag must be
// a pure function of the category, identical through every entry point, so
// the worker's retry-or-give-up decision cannot diverge from what the
// control layer reports to the UI.
func TestRetryableAndPermanentDecisionsConsistent(t *testing.T) {
	categories := []string{
		drive.ErrorCategoryAuth, drive.ErrorCategoryPermission,
		drive.ErrorCategoryRateLimit, drive.ErrorCategoryNetwork,
		drive.ErrorCategoryTimeout, drive.ErrorCategoryRemote5xx,
		drive.ErrorCategoryNotFound, drive.ErrorCategoryConflict,
		drive.ErrorCategoryInvalidRequest, drive.ErrorCategoryLocalIO,
		drive.ErrorCategoryConsistency, drive.ErrorCategoryUnsupported,
		drive.ErrorCategoryPersistence, drive.ErrorCategoryCancelled,
		drive.ErrorCategoryUnknown,
	}
	// Each category is exercised with a natural-language message that the
	// taxonomy actually matches (category names like "rate_limit" are
	// abstract identifiers, not keywords).
	samples := map[string]string{
		drive.ErrorCategoryAuth:           "401 unauthorized",
		drive.ErrorCategoryPermission:     "access denied",
		drive.ErrorCategoryRateLimit:      "429 too many requests",
		drive.ErrorCategoryNetwork:        "connection refused",
		drive.ErrorCategoryTimeout:        "deadline exceeded",
		drive.ErrorCategoryRemote5xx:      "502 bad gateway",
		drive.ErrorCategoryNotFound:       "not found",
		drive.ErrorCategoryConflict:       "already exists",
		drive.ErrorCategoryInvalidRequest: "bad request",
		drive.ErrorCategoryLocalIO:        "no space left",
		drive.ErrorCategoryConsistency:    "hash mismatch",
		drive.ErrorCategoryUnsupported:    "not supported",
		drive.ErrorCategoryPersistence:    "state save failed",
		drive.ErrorCategoryCancelled:      "context canceled",
		drive.ErrorCategoryUnknown:        "something entirely novel",
	}
	for _, cat := range categories {
		msg, ok := samples[cat]
		if !ok {
			t.Fatalf("no sample for category %q", cat)
		}
		want := drive.RetryableCategory(cat)
		viaError := ClassifyError(errors.New(msg))
		if viaError.Retryable != want {
			t.Errorf("category %q: ClassifyError retryable = %t, want %t (msg %q)", cat, viaError.Retryable, want, msg)
		}
		viaMessage := ClassifyErrorMessage(msg)
		if viaMessage.Retryable != want {
			t.Errorf("category %q: ClassifyErrorMessage retryable = %t, want %t (msg %q)", cat, viaMessage.Retryable, want, msg)
		}
		if got := viaError.Category; got != cat {
			t.Errorf("category %q: sample classified as %q", cat, got)
		}
	}
}
