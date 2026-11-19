//go:build fasttest

package drime

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/testsupport/testknobs"
)

func init() {
	// The page-cap regression test only needs to cross the cap; 20 pages of 100
	// entries exercise the same failure path as the production 200 without
	// mapping 20k JSON entries under -race.
	maxListPages = 20
	// Collapse the one-shot settle sleep; it exists so a real provider can catch
	// up, which a fake provider does instantly.
	removeSettleDelay = testknobs.Pause(removeSettleDelay)
	// Keep the poll non-zero to avoid a hot spin if the settle condition is
	// never met; 1ms is still far below the production 150ms.
	childSettleBackoff = time.Millisecond
}
