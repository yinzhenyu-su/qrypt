//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// swapIntoPlace moves the running executable aside, places the new binary at
// target, and arranges for the displaced file to disappear. A running image
// cannot be overwritten in place, so the rename-aside dance is required.
func swapIntoPlace(tmp, target string) error {
	cleanupDisplaced(target)

	displaced := target + ".old"
	if err := os.Rename(target, displaced); err != nil {
		// The conventional name may still be held by an earlier process that
		// has not exited; fall back to a unique name so the upgrade succeeds.
		displaced = fmt.Sprintf("%s.old.%d", target, os.Getpid())
		if uniqueErr := os.Rename(target, displaced); uniqueErr != nil {
			return fmt.Errorf("move running executable aside: %w", uniqueErr)
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		// Put the original back so a failed upgrade leaves a working binary.
		_ = os.Rename(displaced, target)
		return fmt.Errorf("place new executable: %w", err)
	}
	scheduleRemoval(displaced)
	return nil
}

// cleanupDisplaced best-effort removes files left aside by an earlier upgrade,
// so a leftover never blocks the current one.
func cleanupDisplaced(target string) {
	dir := filepath.Dir(target)
	base := filepath.Base(target) + ".old"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == base || strings.HasPrefix(name, base+".") {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// scheduleRemoval deletes the displaced file now, and if it is the running
// image (and therefore still locked) asks Windows to delete it on the next
// reboot.
func scheduleRemoval(path string) {
	if err := os.Remove(path); err == nil {
		return
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	_ = windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}

// rerunAdvice is the copy-pastable command that repeats the upgrade with the
// privilege the replacement needs.
func rerunAdvice(target string) string {
	return fmt.Sprintf("%s upgrade", target)
}
