// Package simread provides a deterministic simulated read provider for tests.
//
// The provider wraps a real [drive.Driver] — for example localfs over a
// temporary directory, or [drive.FakeDriver] — and overrides Read so every
// range request is paced, delayed, faulted and limited on a simulated
// timeline. Simulated durations are divided by [Config.TimeScale] before being
// slept, so a provider configured at 150 KiB/s per connection runs its
// simulated seconds in a fraction of a real second while the production read
// path still observes the configured, unmodified rates, delays and timeouts.
//
// Everything is in-process: no network, no credentials, and no dependence on
// wall-clock rate measurement beyond generous upper bounds. Every range request
// is recorded so tests can assert on exact request counts, offsets, sizes,
// retries, truncation and byte coverage.
//
// A typical use wraps localfs and drives the production read path with it:
//
//	dir := t.TempDir()
//	// write the fixture file under dir, then:
//	p := simread.Wrap(localfs.New(dir), simread.Config{
//		TimeScale:       2,
//		RateBytesPerSec: 150 << 10,
//	})
//
// The package is deliberately test support only: it must not be imported by
// production code.
package simread
