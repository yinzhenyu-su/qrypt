//go:build windows

package install

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	userEnvironmentKey = `Environment`
	userPathValue      = `Path`
)

// AddUserPath appends dir to the user-scope PATH exactly once. It reports
// whether the value changed. The machine-scope value is never touched, so no
// administrator rights are needed.
func AddUserPath(dir string) (bool, error) {
	value, typ, err := readUserPath()
	if err != nil {
		return false, err
	}
	updated, changed := appendPathEntry(value, dir)
	if !changed {
		return false, nil
	}
	if err := writeUserPath(updated, typ); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveUserPath removes exactly the user-scope PATH entry naming dir and
// reports whether it removed one. Every other entry is preserved byte-for-byte.
func RemoveUserPath(dir string) (bool, error) {
	value, typ, err := readUserPath()
	if err != nil {
		return false, err
	}
	updated, removed := removePathEntry(value, dir)
	if !removed {
		return false, nil
	}
	if err := writeUserPath(updated, typ); err != nil {
		return false, err
	}
	return true, nil
}

// appendPathEntry returns value with dir appended unless an equivalent entry is
// already present.
func appendPathEntry(value, dir string) (string, bool) {
	if pathListContains(value, dir) {
		return value, false
	}
	if value == "" {
		return dir, true
	}
	return strings.TrimRight(value, ";") + ";" + dir, true
}

// removePathEntry returns value without the entry naming dir. It keeps the
// remaining entries in their original order and formatting.
func removePathEntry(value, dir string) (string, bool) {
	if value == "" {
		return value, false
	}
	parts := strings.Split(value, ";")
	kept := make([]string, 0, len(parts))
	removed := false
	for _, part := range parts {
		if !removed && samePathEntry(part, dir) {
			removed = true
			continue
		}
		kept = append(kept, part)
	}
	if !removed {
		return value, false
	}
	if len(kept) == 0 {
		return "", true
	}
	return strings.Join(kept, ";"), true
}

// pathListContains reports whether value already names dir.
func pathListContains(value, dir string) bool {
	for _, part := range strings.Split(value, ";") {
		if samePathEntry(part, dir) {
			return true
		}
	}
	return false
}

// samePathEntry compares PATH entries the way Windows does: case-insensitively,
// ignoring a trailing directory separator.
func samePathEntry(entry, dir string) bool {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return false
	}
	entry = strings.TrimRight(entry, `\/`)
	dir = strings.TrimRight(strings.TrimSpace(dir), `\/`)
	if entry == "" || dir == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(entry), filepath.Clean(dir))
}

func readUserPath() (string, uint32, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, userEnvironmentKey, registry.QUERY_VALUE)
	if err != nil {
		return "", 0, err
	}
	defer key.Close()

	value, typ, err := key.GetStringValue(userPathValue)
	if errors.Is(err, registry.ErrNotExist) {
		return "", registry.SZ, nil
	}
	if err != nil {
		return "", 0, err
	}
	if typ != registry.EXPAND_SZ {
		typ = registry.SZ
	}
	return value, typ, nil
}

func writeUserPath(value string, typ uint32) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, userEnvironmentKey,
		registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	if typ == registry.EXPAND_SZ {
		return key.SetExpandStringValue(userPathValue, value)
	}
	return key.SetStringValue(userPathValue, value)
}
