package p115

import (
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) LegacyCredentialFile() string { return "115_cookie.json" }

func (d *Driver) ExportCredentialSession(store drive.StateStore, params drive.Params) ([]byte, string, bool, error) {
	var state cookieState
	if err := store.LoadJSON("115_cookie.json", &state); err != nil {
		return nil, "", false, err
	}
	if state.Cookie == "" {
		return nil, "", false, nil
	}
	configAccount := cookieAccountValue(params["cookie"])
	stateAccount := cookieAccountValue(state.Cookie)
	if configAccount == "" || stateAccount == "" || stateAccount != configAccount {
		return nil, "", false, nil
	}
	return encodeCredentialCookie(state.Cookie), cookieAccountValue(state.Cookie), true, nil
}

func cookieAccountValue(cookie string) string {
	for _, pair := range strings.Split(cookie, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "UID") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
