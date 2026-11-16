package p115open

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ReadPolicy declares the open-platform provider's fetch bounds. The same
// CDN serves 115 open-platform files, and it can pause mid-range on cold
// files or throttle a non-VIP account; a 90 s idle window still bounds a
// truly stuck body.
func (d *Driver) ReadPolicy() drive.ReadPolicy {
	p := drive.DefaultReadPolicy()
	p.Fetch = drive.FetchTimeouts{Idle: 90 * time.Second, Total: 30 * time.Minute}
	return p
}
