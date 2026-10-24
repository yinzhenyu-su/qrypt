package p123

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) LegacyCredentialFile() string { return tokenStateFile }

func (d *Driver) ExportCredentialSession(store drive.StateStore, params drive.Params) ([]byte, string, bool, error) {
	var state tokenState
	if err := store.LoadJSON(tokenStateFile, &state); err != nil {
		return nil, "", false, err
	}
	if state.Token == "" || state.Username != params["username"] {
		return nil, "", false, nil
	}
	if !state.Expire.IsZero() && time.Now().After(state.Expire) {
		return nil, "", false, nil
	}
	payload, err := encodeCredentialPayload(credentialPayload{
		Token:    state.Token,
		Username: state.Username,
		Expire:   state.Expire,
	})
	if err != nil {
		return nil, "", false, err
	}
	return payload, state.Username, true, nil
}
