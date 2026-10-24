package quark

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// quarkShareIDPattern matches a Quark share link id (pwd_id): 12 alphanumerics.
var quarkShareIDPattern = regexp.MustCompile(`^[0-9a-zA-Z]{12}$`)

var _ drive.ShareLinkParser = (*Driver)(nil)

// ParseShareLink recognizes Quark share links
// (https://pan.quark.cn/s/<link-id>[?pwd=xxxx]) and a bare link id. It is what
// lets callers pass a link without naming the provider.
func (d *Driver) ParseShareLink(raw string) (drive.ShareLink, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return drive.ShareLink{}, false
	}
	if !strings.Contains(raw, "://") {
		if !quarkShareIDPattern.MatchString(raw) {
			return drive.ShareLink{}, false
		}
		return drive.ShareLink{URL: raw, LinkID: raw}, true
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return drive.ShareLink{}, false
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return drive.ShareLink{}, false
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if host != "pan.quark.cn" {
		return drive.ShareLink{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "s" || !quarkShareIDPattern.MatchString(parts[1]) {
		return drive.ShareLink{}, false
	}
	return drive.ShareLink{
		URL:      raw,
		LinkID:   parts[1],
		Passcode: parsed.Query().Get("pwd"),
	}, true
}
