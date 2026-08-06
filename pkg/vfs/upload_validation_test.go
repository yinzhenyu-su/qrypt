package vfs

import (
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// The upload engine validates what the driver reported back; every
// mismatch is a data problem that retrying cannot fix, so the error must
// carry drive.ErrInvalidInput for upper layers.
func TestValidateUploadedEntryClassifiesAsInvalidInput(t *testing.T) {
	cases := []struct {
		name  string
		entry drive.Entry
	}{
		{"empty id", drive.Entry{Size: 10, Name: "f"}},
		{"size mismatch", drive.Entry{ID: "x", Size: 11, Name: "f"}},
		{"name mismatch", drive.Entry{ID: "x", Size: 10, Name: "other"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUploadedEntry(tc.entry, "f", 10)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !errors.Is(err, drive.ErrInvalidInput) {
				t.Errorf("validateUploadedEntry(%v) = %v, want drive.ErrInvalidInput", tc.entry, err)
			}
		})
	}

	// A consistent entry validates without error.
	ok := drive.Entry{ID: "x", Size: 10, Name: "f"}
	if err := validateUploadedEntry(ok, "f", 10); err != nil {
		t.Errorf("valid entry rejected: %v", err)
	}
}
