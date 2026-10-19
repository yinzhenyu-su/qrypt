//go:build !windows

package selfupdate

import (
	"fmt"
	"os"
)

// swapIntoPlace renames the prepared binary over the target. On Unix a rename
// over a running image is safe: the running process keeps the old inode.
func swapIntoPlace(tmp, target string) error {
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// rerunAdvice is the copy-pastable command that repeats the upgrade with the
// privilege the atomic rename needs.
func rerunAdvice(target string) string {
	return fmt.Sprintf("sudo %s upgrade", target)
}
