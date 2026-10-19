package selfupdate

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version. qrypt publishes releases as plain
// vMAJOR.MINOR.PATCH tags, so no pre-release or build metadata is modelled.
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion parses a release version such as "v1.2.3" or "1.2.3". It
// rejects anything that is not exactly three non-negative numeric components,
// so a development build identifier such as "v0.5.3-10-g9d16c54" does not
// parse.
func ParseVersion(s string) (Version, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
	}
	var nums [3]int
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}, nil
}

// MustParseVersion is ParseVersion for tests and constants; it panics on a bad
// version.
func MustParseVersion(s string) Version {
	v, err := ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

// IsReleaseVersion reports whether s is a plain release version that can be
// compared against a published release.
func IsReleaseVersion(s string) bool {
	_, err := ParseVersion(s)
	return err == nil
}

// String renders the version with a leading "v".
func (v Version) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare returns a negative number, zero, or a positive number as v is less
// than, equal to, or greater than o.
func (v Version) Compare(o Version) int {
	if c := cmp.Compare(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Minor, o.Minor); c != 0 {
		return c
	}
	return cmp.Compare(v.Patch, o.Patch)
}
