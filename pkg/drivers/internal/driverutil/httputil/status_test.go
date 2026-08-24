package httputil

import (
	"net/http"
	"testing"
)

func TestIsNonRetryableClientStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{name: "bad request", status: http.StatusBadRequest, want: true},
		{name: "conflict", status: http.StatusConflict, want: true},
		{name: "request timeout", status: http.StatusRequestTimeout, want: false},
		{name: "too many requests", status: http.StatusTooManyRequests, want: false},
		{name: "server error", status: http.StatusInternalServerError, want: false},
		{name: "success", status: http.StatusOK, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNonRetryableClientStatus(tt.status); got != tt.want {
				t.Fatalf("IsNonRetryableClientStatus(%d) = %t, want %t", tt.status, got, tt.want)
			}
		})
	}
}
