package util

import "strings"

// TOMLPath renders a filesystem path as a TOML basic string (backslashes and
// quotes escaped). Windows temp paths such as C:\Users\... would otherwise be
// read as \U escapes by the TOML parser.
func TOMLPath(p string) string {
	p = strings.ReplaceAll(p, `\`, `\\`)
	p = strings.ReplaceAll(p, `"`, `\"`)
	return `"` + p + `"`
}
