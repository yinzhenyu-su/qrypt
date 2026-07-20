package core

import (
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type ErrorCode string

const (
	ErrorCodeNetworkRetryable ErrorCode = "network_retryable"
	ErrorCodeAuthExpired      ErrorCode = "auth_expired"
	ErrorCodeNotFound         ErrorCode = "not_found"
	ErrorCodePermission       ErrorCode = "permission"
	ErrorCodeRateLimited      ErrorCode = "rate_limited"
	ErrorCodeLocalIO          ErrorCode = "local_io"
	ErrorCodeCancelled        ErrorCode = "cancelled"
	ErrorCodeUnsupported      ErrorCode = "unsupported"
	ErrorCodeUnknown          ErrorCode = "unknown"
)

type ErrorInfo struct {
	Code      ErrorCode `json:"code"`
	Category  string    `json:"category"`
	Retryable bool      `json:"retryable"`
	Message   string    `json:"message"`
}

func ClassifyError(err error) ErrorInfo {
	if err == nil {
		return ErrorInfo{}
	}
	category := drive.ErrorCategory(err)
	return ErrorInfo{
		Code:      errorCode(category, err.Error()),
		Category:  category,
		Retryable: errorRetryable(category),
		Message:   err.Error(),
	}
}

func ClassifyErrorMessage(message string) ErrorInfo {
	if strings.TrimSpace(message) == "" {
		return ErrorInfo{}
	}
	category := drive.ErrorCategoryMessage(message)
	return ErrorInfo{
		Code:      errorCode(category, message),
		Category:  category,
		Retryable: errorRetryable(category),
		Message:   message,
	}
}

func errorCode(category, message string) ErrorCode {
	lower := strings.ToLower(message)
	switch category {
	case drive.ErrorCategoryAuth:
		if strings.Contains(lower, "forbidden") || strings.Contains(lower, "permission") || strings.Contains(lower, "access denied") || strings.Contains(lower, "403") {
			return ErrorCodePermission
		}
		return ErrorCodeAuthExpired
	case drive.ErrorCategoryRateLimit:
		return ErrorCodeRateLimited
	case drive.ErrorCategoryNetwork, drive.ErrorCategoryTimeout, drive.ErrorCategoryRemote5xx:
		return ErrorCodeNetworkRetryable
	case drive.ErrorCategoryNotFound:
		return ErrorCodeNotFound
	case drive.ErrorCategoryLocalIO:
		return ErrorCodeLocalIO
	case drive.ErrorCategoryCancelled:
		return ErrorCodeCancelled
	case drive.ErrorCategoryUnsupported:
		return ErrorCodeUnsupported
	default:
		return ErrorCodeUnknown
	}
}

func errorRetryable(category string) bool {
	switch category {
	case drive.ErrorCategoryNetwork, drive.ErrorCategoryTimeout, drive.ErrorCategoryRemote5xx, drive.ErrorCategoryRateLimit:
		return true
	default:
		return false
	}
}
