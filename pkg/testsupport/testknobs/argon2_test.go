package testknobs

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/crypt"
)

// TestSetFastArgon2Params checks the cross-package helper installs the tiny
// cost into pkg/crypt and restores the production default afterwards.
func TestSetFastArgon2Params(t *testing.T) {
	original := crypt.DefaultArgon2Params()

	restore := SetFastArgon2Params()
	defer restore()

	if got := crypt.DefaultArgon2Params(); got != FastArgon2 {
		t.Fatalf("DefaultArgon2Params() = %+v, want FastArgon2 %+v", got, FastArgon2)
	}

	restore()
	if got := crypt.DefaultArgon2Params(); got != original {
		t.Fatalf("after restore DefaultArgon2Params() = %+v, want %+v", got, original)
	}
}
