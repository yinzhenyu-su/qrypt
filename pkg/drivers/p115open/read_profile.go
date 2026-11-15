package p115open

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ReadProfile declares the open-platform provider's fetch bounds. The same
// CDN serves 115 open-platform files, and it can pause mid-range on cold
// files or throttle a non-VIP account; a 90 s idle window still bounds a
// truly stuck body.
func (d *Driver) ReadProfile() drive.ReadProfile {
	return drive.ReadProfile{
		FetchIdleTimeout:  90 * time.Second,
		FetchTotalTimeout: 30 * time.Minute,
	}
}
