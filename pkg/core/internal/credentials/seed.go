package credentials

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// ParamsSeed fingerprints the static params of a credential definition. A
// session is reused only while the seed matches, so replacing a cookie (for
// example switching accounts) discards every mount's rotated session at once.
func ParamsSeed(driverType string, params map[string]string) string {
	pairs := make([]string, 0, len(params))
	for key, value := range params {
		pairs = append(pairs, key+"="+value)
	}
	sort.Strings(pairs)
	sum := sha256.Sum256([]byte(driverType + "\x00" + strings.Join(pairs, "\x00")))
	return hex.EncodeToString(sum[:])
}
