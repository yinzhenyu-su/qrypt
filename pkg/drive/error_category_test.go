package drive

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
	"testing"
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "temporary timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}

func TestErrorCategory(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "cancelled", err: context.Canceled, want: ErrorCategoryCancelled},
		{name: "deadline", err: context.DeadlineExceeded, want: ErrorCategoryTimeout},
		{name: "net timeout", err: timeoutError{}, want: ErrorCategoryTimeout},
		{name: "space unsupported", err: ErrSpaceUnsupported, want: ErrorCategoryUnsupported},
		{name: "not found", err: fmt.Errorf("wrapped: %w", fs.ErrNotExist), want: ErrorCategoryNotFound},
		{name: "exists", err: fmt.Errorf("wrapped: %w", fs.ErrExist), want: ErrorCategoryConflict},
		{name: "path error", err: &fs.PathError{Op: "open", Path: "/tmp/missing", Err: os.ErrPermission}, want: ErrorCategoryLocalIO},
		{name: "auth text", err: fmt.Errorf("quark list: HTTP 401 unauthorized"), want: ErrorCategoryAuth},
		{name: "rate limit text", err: fmt.Errorf("upload: too many requests 429"), want: ErrorCategoryRateLimit},
		{name: "consistency text", err: fmt.Errorf("content mismatch: got 1 byte, want 2"), want: ErrorCategoryConsistency},
		{name: "unknown", err: fmt.Errorf("provider returned unusual response"), want: ErrorCategoryUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorCategory(tc.err); got != tc.want {
				t.Fatalf("ErrorCategory(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestErrorCategoryMessage(t *testing.T) {
	tests := []struct {
		message string
		want    string
	}{
		{message: "", want: ""},
		{message: "gateway timeout from remote 504", want: ErrorCategoryTimeout},
		{message: "service unavailable 503", want: ErrorCategoryRemote5xx},
		{message: "invalid parameter parent_id", want: ErrorCategoryInvalidRequest},
		{message: "driver does not support upload", want: ErrorCategoryUnsupported},
		{message: "connection reset by peer", want: ErrorCategoryNetwork},
		{message: `Get "https://dl-pc-sz.drive.quark.cn/file?auth_key=token": dial tcp: lookup dl-pc-sz.drive.quark.cn: no such host`, want: ErrorCategoryNetwork},
		{message: "read encrypted block: read tcp 192.168.1.32:40128->222.186.17.199:443: read: software caused connection abort", want: ErrorCategoryNetwork},
		{message: "no space left on device", want: ErrorCategoryLocalIO},
	}
	for _, tc := range tests {
		t.Run(tc.message, func(t *testing.T) {
			if got := ErrorCategoryMessage(tc.message); got != tc.want {
				t.Fatalf("ErrorCategoryMessage(%q) = %q, want %q", tc.message, got, tc.want)
			}
		})
	}
}
