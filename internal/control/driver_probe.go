package control

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func driverProbeRootID(ctx context.Context, d drive.Driver) string {
	if drive.HasCapability(d, drive.CapabilityPathResolver) {
		if rootID, err := d.ResolvePath(ctx, "/"); err == nil && rootID != "" {
			return rootID
		}
	}
	for _, candidate := range []string{"", "root", "-11", "0"} {
		entries, err := d.List(ctx, candidate)
		if err != nil {
			continue
		}
		if candidate != "" {
			return candidate
		}
		if len(entries) > 0 && entries[0].ParentID != "" {
			return entries[0].ParentID
		}
		return ""
	}
	return "root"
}

func cleanupProbeDir(ctx context.Context, d drive.Driver, dir drive.Entry) {
	_ = d.Remove(ctx, dir)
}
