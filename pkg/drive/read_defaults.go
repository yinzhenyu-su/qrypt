package drive

import "time"

// ReadChunkSize is an internal cache-alignment unit, not a configuration unit.
const ReadChunkSize = 512 << 10
const DefaultFetchIdleTimeout = 30 * time.Second
const DefaultFetchTotalTimeout = 5 * time.Minute
