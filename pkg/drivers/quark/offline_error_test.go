package quark

import (
	"strconv"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestOfflineErrorFromCode maps the Quark business codes the offline flow can
// hit onto qrypt's stable offline codes, and pins the retryable flag so quota,
// policy, and duplicate failures never look transient.
func TestOfflineErrorFromCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code      int
		want      drive.OfflineErrorCode
		retryable bool
	}{
		{31001, drive.OfflineErrAuthRequired, false},
		{31004, drive.OfflineErrAuthRequired, false},
		{43004, drive.OfflineErrAuthRequired, false},
		{32005, drive.OfflineErrQuotaExceeded, false},
		{32006, drive.OfflineErrQuotaExceeded, false},
		{32018, drive.OfflineErrQuotaExceeded, false},
		{32022, drive.OfflineErrQuotaExceeded, false},
		{25005, drive.OfflineErrSourceInvalid, false},
		{14014, drive.OfflineErrSourceForbidden, false},
		{14016, drive.OfflineErrSourceForbidden, false},
		{22001, drive.OfflineErrSourceForbidden, false},
		{32003, drive.OfflineErrSpaceInsufficient, false},
		{32004, drive.OfflineErrFileTooLarge, false},
		{25003, drive.OfflineErrFileTooLarge, false},
		{25004, drive.OfflineErrFileTooLarge, false},
		{32007, drive.OfflineErrDuplicate, false},
		{15000, drive.OfflineErrProviderUnavailable, true},
		{14013, drive.OfflineErrProviderTaskMissing, true},
		{31007, drive.OfflineErrCrypto, false},
	}
	for _, tc := range cases {
		got := offlineErrorFromCode(tc.code, "boom")
		if got.Code != tc.want {
			t.Errorf("code %d = %q, want %q", tc.code, got.Code, tc.want)
		}
		if got.ProviderCode != strconv.Itoa(tc.code) {
			t.Errorf("code %d provider code = %q", tc.code, got.ProviderCode)
		}
		if got.Retryable != tc.retryable {
			t.Errorf("code %d retryable = %v, want %v", tc.code, got.Retryable, tc.retryable)
		}
	}
}

func TestOfflineErrorFromUnknownCodeIsGeneric(t *testing.T) {
	t.Parallel()
	got := offlineErrorFromCode(999999, "mystery")
	if got.Code != drive.OfflineErrProvider {
		t.Fatalf("unknown code = %q, want %q", got.Code, drive.OfflineErrProvider)
	}
	if got.Retryable {
		t.Fatal("an unmapped provider code must not default to retryable")
	}
}
