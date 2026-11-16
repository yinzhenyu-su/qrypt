package p115

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ReadPolicy declares a longer fetch bound than the conservative default.
// 115 CDN nodes can pause for tens of seconds mid-range on cold files, and
// non-VIP accounts are throttled; the incident that motivated these bounds
// stalled for over a minute. A 90 s idle window still bounds a stuck body
// while giving a slow node room to resume.
func (d *Driver) ReadPolicy() drive.ReadPolicy {
	p := drive.DefaultReadPolicy()
	p.Fetch = drive.FetchTimeouts{Idle: 90 * time.Second, Total: 30 * time.Minute}
	return p
}
