package baidunetdisk

import "github.com/yinzhenyu/qrypt/pkg/drive"

func (d *Driver) LegacyCredentialFile() string { return "baidu_netdisk_token.json" }

func (d *Driver) ExportCredentialSession(store drive.StateStore, params drive.Params) ([]byte, string, bool, error) {
	var state tokenState
	if err := store.LoadJSON("baidu_netdisk_token.json", &state); err != nil {
		return nil, "", false, err
	}
	if state.AccessToken == "" && state.RefreshToken == "" {
		return nil, "", false, nil
	}
	if state.ConfigRefreshToken != "" && params["refresh_token"] != "" && state.ConfigRefreshToken != params["refresh_token"] {
		return nil, "", false, nil
	}
	payload, _, err := encodeCredentialPayload(credentialPayload{
		AccessToken:  state.AccessToken,
		RefreshToken: state.RefreshToken,
		ExpiresAt:    state.ExpiresAt,
	})
	if err != nil {
		return nil, "", false, err
	}
	return payload, "", true, nil
}
