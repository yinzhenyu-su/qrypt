package core

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/util"
)

// defaultOfflinePollInterval is the floor of the offline provider observation
// interval. The runner raises it to the provider hint (capped at
// offlinePollIntervalMax) and backs off on transient errors.
const (
	defaultOfflinePollInterval = 5 * time.Second
	offlinePollIntervalMax     = 30 * time.Second
)

func offlineNow() time.Time { return util.Now() }

func (c *Core) offlinePollEvery() time.Duration {
	if c != nil && c.offlinePollInterval > 0 {
		return c.offlinePollInterval
	}
	return defaultOfflinePollInterval
}

func newOfflineTaskID(fetch bool) string {
	prefix := "offline-"
	if fetch {
		prefix = "offline-fetch-"
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + strconv.FormatInt(util.Now().UnixNano(), 36)
	}
	return prefix + hex.EncodeToString(b[:])
}
