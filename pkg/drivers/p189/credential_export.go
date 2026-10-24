package p189

import "github.com/yinzhenyu/qrypt/pkg/drive"

func (d *Driver) LegacyCredentialFile() string { return "189_cookie.json" }

func (d *Driver) ExportCredentialSession(store drive.StateStore, params drive.Params) ([]byte, string, bool, error) {
	var state cookieState
	if err := store.LoadJSON("189_cookie.json", &state); err != nil {
		return nil, "", false, err
	}
	if state.Cookie == "" {
		return nil, "", false, nil
	}
	payload, err := encodeCredentialPayload(credentialPayload{
		Cookie:                  state.Cookie,
		PasswordReloginFailedAt: state.PasswordReloginFailedAt,
		PasswordReloginError:    state.PasswordReloginError,
	})
	if err != nil {
		return nil, "", false, err
	}
	return payload, params["username"], true, nil
}
