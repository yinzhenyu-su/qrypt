package testknobs

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/crypt"
)

// TestSetFastScryptParamsInstallsAndRestores proves the helper installs the
// minimal cost that pkg/crypt reads and restores the production default.
func TestSetFastScryptParamsInstallsAndRestores(t *testing.T) {
	original := crypt.DefaultScryptParams()

	restore := SetFastScryptParams()
	if got := crypt.DefaultScryptParams(); got != FastScrypt {
		restore()
		t.Fatalf("DefaultScryptParams() = %+v, want %+v", got, FastScrypt)
	}
	restore()
	if got := crypt.DefaultScryptParams(); got != original {
		t.Fatalf("after restore DefaultScryptParams() = %+v, want %+v", got, original)
	}
}
