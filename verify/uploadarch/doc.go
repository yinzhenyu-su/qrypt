// Package uploadarch holds throwaway experiments that check claims about the
// upload scheduling design: that the persisted retry deadline is a second
// source of truth, that two clock domains meet in the scheduler, and that the
// identity field lists are maintained by hand.
//
// Nothing here is production code. The package exists so the claims behind the
// design review are reproducible instead of argued: every test prints the
// numbers it measured, and every assertion is a claim with an expected value,
// not a timing race.
package uploadarch
