package yun139

import "github.com/yinzhenyu/qrypt/pkg/drive"

func (d *Driver) LegacyCredentialFile() string { return "yun139_auth.json" }

func (d *Driver) ExportCredentialSession(store drive.StateStore, params drive.Params) ([]byte, string, bool, error) {
	var state authState
	if err := store.LoadJSON("yun139_auth.json", &state); err != nil {
		return nil, "", false, err
	}
	if state.Authorization == "" {
		return nil, "", false, nil
	}
	if state.ConfigAuthorization != "" && params["authorization"] != "" && state.ConfigAuthorization != params["authorization"] {
		return nil, "", false, nil
	}
	payload, err := encodeCredentialAuthorization(state.Authorization)
	if err != nil {
		return nil, "", false, err
	}
	return payload, "", true, nil
}
