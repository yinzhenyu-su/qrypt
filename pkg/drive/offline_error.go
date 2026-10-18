package drive

import (
	"errors"
	"fmt"
)

// OfflineErrorCode is a stable, provider-neutral classification of an offline
// download failure. It is part of the API: CLI scripts and the task detail
// surface match on these values, so they must not change.
type OfflineErrorCode string

const (
	OfflineErrAuthRequired         OfflineErrorCode = "offline_auth_required"
	OfflineErrQuotaExceeded        OfflineErrorCode = "offline_quota_exceeded"
	OfflineErrSourceInvalid        OfflineErrorCode = "offline_source_invalid"
	OfflineErrSourceForbidden      OfflineErrorCode = "offline_source_forbidden"
	OfflineErrSpaceInsufficient    OfflineErrorCode = "offline_space_insufficient"
	OfflineErrFileTooLarge         OfflineErrorCode = "offline_file_too_large"
	OfflineErrDuplicate            OfflineErrorCode = "offline_duplicate"
	OfflineErrSelectionUnsupported OfflineErrorCode = "offline_selection_unsupported"
	OfflineErrSourceUnsupported    OfflineErrorCode = "offline_source_unsupported"
	OfflineErrInspectUnsupported   OfflineErrorCode = "offline_inspect_unsupported"
	OfflineErrProviderUnavailable  OfflineErrorCode = "offline_provider_unavailable"
	OfflineErrProviderTaskMissing  OfflineErrorCode = "offline_provider_task_missing"
	OfflineErrSameDestination      OfflineErrorCode = "offline_same_destination"
	OfflineErrPathInvalid          OfflineErrorCode = "offline_path_invalid"
	OfflineErrWaitTimeout          OfflineErrorCode = "offline_wait_timeout"
	OfflineErrCrypto               OfflineErrorCode = "offline_crypto_error"
	OfflineErrProvider             OfflineErrorCode = "offline_provider_error"
)

// OfflineError is a provider-neutral offline failure. ProviderCode preserves
// the provider's own numeric code for diagnostics; Code is what callers match
// on.
type OfflineError struct {
	Code         OfflineErrorCode
	ProviderCode string
	Message      string
	Retryable    bool
}

func (e *OfflineError) Error() string {
	if e == nil {
		return ""
	}
	message := e.Message
	if message == "" {
		message = string(e.Code)
	}
	if e.ProviderCode != "" {
		return fmt.Sprintf("%s (provider code %s): %s", e.Code, e.ProviderCode, message)
	}
	return fmt.Sprintf("%s: %s", e.Code, message)
}

// NewOfflineError builds a classified offline error, deriving Retryable from
// the code so callers never have to re-derive it from the message.
func NewOfflineError(code OfflineErrorCode, providerCode, message string) *OfflineError {
	return &OfflineError{
		Code:         code,
		ProviderCode: providerCode,
		Message:      message,
		Retryable:    OfflineRetryable(code),
	}
}

// OfflineRetryable reports whether a code is worth retrying. Only transient
// provider problems are retryable; quota, policy, auth, unsupported, and
// crypto failures are deterministic and must not be retried automatically.
func OfflineRetryable(code OfflineErrorCode) bool {
	switch code {
	case OfflineErrProviderUnavailable, OfflineErrProviderTaskMissing:
		return true
	default:
		return false
	}
}

// AsOfflineError extracts a classified offline error from err.
func AsOfflineError(err error) (*OfflineError, bool) {
	var offline *OfflineError
	if errors.As(err, &offline) {
		return offline, true
	}
	return nil, false
}
