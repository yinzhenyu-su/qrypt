package contracttest

import "testing"

func TestReadOnlyProbeClassifier(t *testing.T) {
	cases := []struct {
		test string
		req  DriverTestRequest
		want bool
	}{
		{"read", DriverTestRequest{Path: "/movie.mp4"}, true},
		{"read", DriverTestRequest{}, false},
		{"tuneread", DriverTestRequest{Path: "/movie.mp4"}, true},
		{"tuneread", DriverTestRequest{}, false},
		{"crud", DriverTestRequest{}, false},
		{"upload", DriverTestRequest{}, false},
		{"fs", DriverTestRequest{Path: "/movie.mp4"}, false},
	}
	for _, tc := range cases {
		if got := ReadOnlyProbe(tc.test, tc.req); got != tc.want {
			t.Fatalf("ReadOnlyProbe(%q, path=%q) = %v, want %v", tc.test, tc.req.Path, got, tc.want)
		}
	}
}
