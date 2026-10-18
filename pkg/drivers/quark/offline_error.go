package quark

import (
	"strconv"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// quarkOfflineErrorCodes maps Quark's business codes onto qrypt's stable,
// provider-neutral offline codes. The table mirrors the client's own error
// map (quota, parsing, violation, space, size, duplicate) plus the transient
// and missing-task cases verified against the live API.
var quarkOfflineErrorCodes = map[int]drive.OfflineErrorCode{
	31001: drive.OfflineErrAuthRequired,
	31004: drive.OfflineErrAuthRequired,
	43004: drive.OfflineErrAuthRequired,

	32005: drive.OfflineErrQuotaExceeded,
	32006: drive.OfflineErrQuotaExceeded,
	32018: drive.OfflineErrQuotaExceeded,
	32022: drive.OfflineErrQuotaExceeded,

	25005: drive.OfflineErrSourceInvalid,

	14014: drive.OfflineErrSourceForbidden,
	14016: drive.OfflineErrSourceForbidden,
	22001: drive.OfflineErrSourceForbidden,

	32003: drive.OfflineErrSpaceInsufficient,

	32004: drive.OfflineErrFileTooLarge,
	25003: drive.OfflineErrFileTooLarge,
	25004: drive.OfflineErrFileTooLarge,

	32007: drive.OfflineErrDuplicate,

	15000: drive.OfflineErrProviderUnavailable,

	14013: drive.OfflineErrProviderTaskMissing,

	31007: drive.OfflineErrCrypto,
}

// offlineErrorFromCode classifies a Quark business code. Unmapped codes become
// the generic non-retryable provider error rather than being silently treated
// as transient.
func offlineErrorFromCode(code int, message string) *drive.OfflineError {
	mapped, ok := quarkOfflineErrorCodes[code]
	if !ok {
		mapped = drive.OfflineErrProvider
	}
	return drive.NewOfflineError(mapped, strconv.Itoa(code), message)
}
