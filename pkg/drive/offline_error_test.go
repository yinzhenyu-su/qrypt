package drive

import (
	"errors"
	"fmt"
	"testing"
)

func TestOfflineRetryableClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code OfflineErrorCode
		want bool
	}{
		{OfflineErrProviderUnavailable, true},
		{OfflineErrProviderTaskMissing, true},
		{OfflineErrQuotaExceeded, false},
		{OfflineErrSourceInvalid, false},
		{OfflineErrSourceForbidden, false},
		{OfflineErrSpaceInsufficient, false},
		{OfflineErrFileTooLarge, false},
		{OfflineErrDuplicate, false},
		{OfflineErrSelectionUnsupported, false},
		{OfflineErrSourceUnsupported, false},
		{OfflineErrInspectUnsupported, false},
		{OfflineErrAuthRequired, false},
		{OfflineErrCrypto, false},
		{OfflineErrSameDestination, false},
		{OfflineErrWaitTimeout, false},
		{OfflineErrProvider, false},
	}
	for _, tc := range cases {
		if got := OfflineRetryable(tc.code); got != tc.want {
			t.Errorf("OfflineRetryable(%q) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestNewOfflineErrorCarriesCodeAndRetryable(t *testing.T) {
	t.Parallel()
	err := NewOfflineError(OfflineErrQuotaExceeded, "32005", "download quota exhausted")
	if err.Code != OfflineErrQuotaExceeded {
		t.Fatalf("code = %q, want %q", err.Code, OfflineErrQuotaExceeded)
	}
	if err.ProviderCode != "32005" {
		t.Fatalf("provider code = %q, want 32005", err.ProviderCode)
	}
	if err.Retryable {
		t.Fatal("quota exhaustion must not be retryable")
	}
	if got := err.Error(); got == "" {
		t.Fatal("Error() must not be empty")
	}

	transient := NewOfflineError(OfflineErrProviderUnavailable, "15000", "")
	if !transient.Retryable {
		t.Fatal("provider unavailable must be retryable")
	}
	if got := transient.Error(); got == "" {
		t.Fatal("Error() must not be empty")
	}
}

func TestAsOfflineErrorAcrossWrapping(t *testing.T) {
	t.Parallel()
	base := NewOfflineError(OfflineErrDuplicate, "32007", "task already exists")
	wrapped := fmt.Errorf("quark: offline submit: %w", base)
	got, ok := AsOfflineError(wrapped)
	if !ok {
		t.Fatal("AsOfflineError did not unwrap the classified error")
	}
	if got.Code != OfflineErrDuplicate {
		t.Fatalf("code = %q, want %q", got.Code, OfflineErrDuplicate)
	}
	if _, ok := AsOfflineError(errors.New("plain")); ok {
		t.Fatal("AsOfflineError matched an unclassified error")
	}
}
