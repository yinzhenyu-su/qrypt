package debug

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/contracttest"
)

func TestReadMeasurementUsesByteWindow(t *testing.T) {
	cmd := &cobra.Command{}
	addDebugDriverTestFlags(cmd, "read")
	if cmd.Flags().Lookup("window") == nil || cmd.Flags().Lookup("window-chunks") != nil {
		t.Fatal("measurement retained chunk flag")
	}
	for _, tc := range []struct {
		bytes int64
		valid bool
	}{{0, true}, {1, true}, {8 << 20, true}, {32 << 20, true}, {-1, false}, {33 << 20, false}} {
		err := ValidateDriverTestRequest(contracttest.DriverTestRequest{Test: "read", Mount: "cloud", MountPoint: t.TempDir(), CacheMode: "cold", Samples: 1, WindowBytes: tc.bytes})
		if (err == nil) != tc.valid {
			t.Fatalf("window=%d error=%v", tc.bytes, err)
		}
	}
}
