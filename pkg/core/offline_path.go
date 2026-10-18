package core

import (
	"fmt"
	"strings"
)

// offlineMaxPathDepth bounds how deep a provider-reported staged path may nest
// under the destination, so a hostile tree cannot exhaust the filesystem.
const offlineMaxPathDepth = 32

// offlinePathInvalidError builds the typed failure for a staged path that
// cannot be placed safely at the destination. It classifies as
// offline_path_invalid and never retries.
func offlinePathInvalidError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrOfflinePathInvalid, fmt.Sprintf(format, args...))
}

// sanitizeOfflineRelPath validates a provider-reported staged relative path and
// returns its components. Absolute paths, drive prefixes, empty or traversing
// components, control characters, Windows-reserved names, and trailing dots or
// spaces are rejected.
func sanitizeOfflineRelPath(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, offlinePathInvalidError("staged path is empty")
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "\\") {
		return nil, offlinePathInvalidError("staged path %q is absolute", raw)
	}
	if len(raw) >= 2 && raw[1] == ':' {
		return nil, offlinePathInvalidError("staged path %q has a drive prefix", raw)
	}
	parts := splitStagedPath(raw)
	if len(parts) > offlineMaxPathDepth {
		return nil, offlinePathInvalidError("staged path %q is too deep", raw)
	}
	for _, part := range parts {
		if err := validateOfflineComponent(part); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

// sanitizeOfflineComponent validates a single file name used for placement when
// no staged path is available or a flat layout was requested. A name that
// carries a path separator is rejected rather than silently nested.
func sanitizeOfflineComponent(raw string) (string, error) {
	parts := splitStagedPath(raw)
	if len(parts) != 1 {
		return "", offlinePathInvalidError("staged file name %q contains a path separator", raw)
	}
	if err := validateOfflineComponent(parts[0]); err != nil {
		return "", err
	}
	return parts[0], nil
}

// splitStagedPath splits on both separators so a Windows-style path from a
// provider cannot smuggle a component past validation. Empty components are
// preserved so they can be rejected.
func splitStagedPath(raw string) []string {
	parts := make([]string, 0, 4)
	var current strings.Builder
	for _, r := range raw {
		if r == '/' || r == '\\' {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteRune(r)
	}
	parts = append(parts, current.String())
	return parts
}

// validateOfflineComponent rejects one path component that cannot be placed
// safely under a destination on any supported platform.
func validateOfflineComponent(part string) error {
	if part == "" {
		return offlinePathInvalidError("staged path has an empty component")
	}
	if part == "." || part == ".." {
		return offlinePathInvalidError("staged path component %q is not allowed", part)
	}
	if strings.ContainsAny(part, `<>:"|?*`) {
		return offlinePathInvalidError("staged path component %q contains a reserved character", part)
	}
	for _, r := range part {
		if r < 0x20 || r == 0x7f {
			return offlinePathInvalidError("staged path component %q contains a control character", part)
		}
	}
	if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
		return offlinePathInvalidError("staged path component %q ends with a dot or space", part)
	}
	if offlineReservedWindowsName(part) {
		return offlinePathInvalidError("staged path component %q is a reserved name", part)
	}
	if len(part) > 255 {
		return offlinePathInvalidError("staged path component %q is too long", part)
	}
	return nil
}

// offlineReservedWindowsNames are the device names Windows refuses as file
// names, matched case-insensitively and ignoring an extension.
var offlineReservedWindowsNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

func offlineReservedWindowsName(part string) bool {
	base := part
	if idx := strings.IndexByte(base, '.'); idx >= 0 {
		base = base[:idx]
	}
	return offlineReservedWindowsNames[strings.ToUpper(base)]
}
