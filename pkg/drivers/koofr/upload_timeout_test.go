package koofr

import (
	"testing"
	"time"
)

func TestUploadTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int64
		want time.Duration
	}{
		{name: "empty", size: 0, want: koofrUploadMinTimeout},
		{name: "small", size: 100 << 20, want: koofrUploadMinTimeout},
		{name: "five gigabytes", size: 5142406828, want: time.Duration(5142406828/koofrUploadMinRate) * time.Second},
		{name: "larger than the cap", size: 1 << 50, want: koofrUploadMaxTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := uploadTimeout(tc.size); got != tc.want {
				t.Fatalf("uploadTimeout(%d) = %s, want %s", tc.size, got, tc.want)
			}
		})
	}
}
