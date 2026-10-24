package quark

import "github.com/yinzhenyu/qrypt/pkg/drive"

// LegacyCredentialFile is the per-mount rotation state migrated into a shared
// credential.
func (d *Driver) LegacyCredentialFile() string { return "quark_cookie.json" }

func (d *Driver) ExportCredentialSession(store drive.StateStore, params drive.Params) ([]byte, string, bool, error) {
	var state cookieState
	if err := store.LoadJSON("quark_cookie.json", &state); err != nil {
		return nil, "", false, err
	}
	if state.Cookie == "" {
		return nil, "", false, nil
	}
	if configAccount := cookieAccount(params["cookie"]); configAccount != "" {
		if stateAccount := cookieAccount(state.Cookie); stateAccount != "" && stateAccount != configAccount {
			return nil, "", false, nil
		}
	}
	return encodeCredentialCookie(state.Cookie), cookieAccount(state.Cookie), true, nil
}
