package quark

import (
	"os"
	"testing"
)

// TestMain collapses the request retry backoff for the whole package. Several
// tests model transient provider outages (sortFail answering 503) and must
// still exercise the retry loop and its attempt counting, but the real
// exponential pause made the package spend over ten seconds sleeping. Setting
// the package-level hook before m.Run keeps the parallel tests race-free.
func TestMain(m *testing.M) {
	retrySleep = func(int) {}
	os.Exit(m.Run())
}
