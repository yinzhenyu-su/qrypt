package drive

import (
	"errors"
	"fmt"
)

// ShareErrorCode is a stable, provider-neutral classification of a share
// failure. It is part of the API: the CLI and task detail match on these
// values, so they must not change.
type ShareErrorCode string

const (
	// ShareErrLinkInvalid means the link cannot be parsed or no configured
	// driver recognizes it.
	ShareErrLinkInvalid ShareErrorCode = "share_link_invalid"
	// ShareErrLinkProviderMismatch means the acting mount belongs to a
	// different provider than the link.
	ShareErrLinkProviderMismatch ShareErrorCode = "share_link_provider_mismatch"
	// ShareErrCrossProviderUnsupported means the destination mount is a
	// different provider (a native save only exists inside one provider).
	ShareErrCrossProviderUnsupported ShareErrorCode = "share_cross_provider_unsupported"
	// ShareErrUnsupported means the driver does not implement the requested
	// share operation or policy option.
	ShareErrUnsupported ShareErrorCode = "share_unsupported"
	// ShareErrNotFound means the link (or its content) no longer exists.
	ShareErrNotFound ShareErrorCode = "share_not_found"
	// ShareErrExpired means the link expired.
	ShareErrExpired ShareErrorCode = "share_expired"
	// ShareErrPasscodeRequired means the link needs a passcode.
	ShareErrPasscodeRequired ShareErrorCode = "share_passcode_required"
	// ShareErrPasscodeWrong means the supplied passcode was rejected.
	ShareErrPasscodeWrong ShareErrorCode = "share_passcode_wrong"
	// ShareErrOwnSave means the provider forbids saving one's own link.
	ShareErrOwnSave ShareErrorCode = "share_own_save"
	// ShareErrTicketInvalid means an entry's save ticket is missing, stale, or
	// does not match the entry.
	ShareErrTicketInvalid ShareErrorCode = "share_ticket_invalid"
	// ShareErrVisitLimit means the link's visit limit was reached.
	ShareErrVisitLimit ShareErrorCode = "share_visit_limit"
	// ShareErrQuota means the account has no space, or the provider's save
	// count limit was reached.
	ShareErrQuota ShareErrorCode = "share_quota"
	// ShareErrVerificationRequired means the provider demands a human
	// verification step (captcha, SMS) that a stored credential cannot satisfy.
	ShareErrVerificationRequired ShareErrorCode = "share_verification_required"
	// ShareErrRateLimited means the provider throttled the account.
	ShareErrRateLimited ShareErrorCode = "share_rate_limited"
	// ShareErrTimeout means synchronous provider work did not finish inside its
	// bounded window.
	ShareErrTimeout ShareErrorCode = "share_timeout"
	// ShareErrDestinationUnverified means qrypt could not confirm inside its
	// bounded window that the provider actually created the saved entries.
	ShareErrDestinationUnverified ShareErrorCode = "share_destination_unverified"
	// ShareErrProvider is an unclassified provider failure: the provider
	// rejected the call without a code qrypt maps onto a neutral one.
	ShareErrProvider ShareErrorCode = "share_provider_error"
)

// ShareErrorClass is the transport-level class of a share failure. Core and
// control use it to pick an HTTP status and a process exit code without
// parsing provider text.
type ShareErrorClass string

const (
	ShareErrorUsage    ShareErrorClass = "usage"
	ShareErrorNotFound ShareErrorClass = "not_found"
	ShareErrorRuntime  ShareErrorClass = "runtime"
)

// ShareError is a provider-neutral share failure. ProviderCode preserves the
// provider's own code for diagnostics; Code is what callers match on.
type ShareError struct {
	Code         ShareErrorCode  `json:"code"`
	Class        ShareErrorClass `json:"class,omitempty"`
	ProviderCode string          `json:"provider_code,omitempty"`
	Message      string          `json:"message,omitempty"`
	Retryable    bool            `json:"retryable,omitempty"`
}

func (e *ShareError) Error() string {
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

// NewShareError builds a classified share error, deriving Class and Retryable
// from the code so callers never re-derive them from the message.
func NewShareError(code ShareErrorCode, providerCode, message string) *ShareError {
	return &ShareError{
		Code:         code,
		Class:        ShareErrorClassFor(code),
		ProviderCode: providerCode,
		Message:      message,
		Retryable:    ShareRetryable(code),
	}
}

// ShareErrorClassFor maps a code to its transport class.
func ShareErrorClassFor(code ShareErrorCode) ShareErrorClass {
	switch code {
	case ShareErrLinkInvalid, ShareErrLinkProviderMismatch, ShareErrCrossProviderUnsupported,
		ShareErrUnsupported:
		return ShareErrorUsage
	case ShareErrNotFound, ShareErrExpired:
		return ShareErrorNotFound
	default:
		return ShareErrorRuntime
	}
}

// ShareRetryable reports whether a code is worth retrying. Only transient
// provider problems are retryable; policy, passcode, quota, ticket, and
// verification failures are deterministic and must not be retried
// automatically.
func ShareRetryable(code ShareErrorCode) bool {
	switch code {
	case ShareErrRateLimited, ShareErrTimeout:
		return true
	default:
		return false
	}
}

// AsShareError extracts a classified share error from err.
func AsShareError(err error) (*ShareError, bool) {
	var share *ShareError
	if errors.As(err, &share) {
		return share, true
	}
	return nil, false
}

// IsShareErrorCode reports whether err carries the given neutral code.
func IsShareErrorCode(err error, code ShareErrorCode) bool {
	share, ok := AsShareError(err)
	return ok && share.Code == code
}
