package buildinfo

import "testing"

func TestCurrentUsesInjectedValues(t *testing.T) {
	oldVersion, oldCommit, oldTime, oldDirty := buildVersion, buildCommit, buildTime, buildDirty
	t.Cleanup(func() {
		buildVersion, buildCommit, buildTime, buildDirty = oldVersion, oldCommit, oldTime, oldDirty
	})
	buildVersion = "v1.2.3"
	buildCommit = "abc123"
	buildTime = "2026-08-12T09:10:11Z"
	buildDirty = "true"

	info := Current()
	if info.Version != buildVersion || info.Commit != buildCommit || info.BuildTime != buildTime || !info.Dirty {
		t.Fatalf("Current() = %+v", info)
	}
}
