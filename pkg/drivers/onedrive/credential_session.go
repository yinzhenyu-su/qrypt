package onedrive

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

func (d *Driver) loadCredentialSession() {
	d.cred.Load()
}

func (d *Driver) subscribeCredentialSession() {
	d.cred.Subscribe()
}

// applyCredentialBinding applies one decoded token pair to the client.
func (d *Driver) applyCredentialBinding(value credentialPayload, _ drive.CredentialSnapshot, source string) bool {
	if value.AccessToken == "" && value.RefreshToken == "" {
		return false
	}
	d.setTokens(value.AccessToken, value.RefreshToken)
	d.tokenSource = source
	return true
}

// loadAccount records the provider account for diagnostics. App mode already
// knows the email; delegated (personal) mode asks Graph once, best-effort.
func (d *Driver) loadAccount(ctx context.Context) {
	if d.email != "" {
		d.account = d.email
		return
	}
	if d.appMode {
		return
	}
	var me struct {
		ID                string `json:"id"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := d.requestJSON(ctx, http.MethodGet, d.apiBaseURL+"/v1.0/me?$select=id,userPrincipalName", nil, &me); err != nil {
		logging.L.Warnf("[onedrive] fetch account: %v", err)
		return
	}
	if me.UserPrincipalName != "" {
		d.account = me.UserPrincipalName
	} else {
		d.account = me.ID
	}
}

func (d *Driver) accountID() string {
	if d.account != "" {
		return d.account
	}
	return d.email
}

type credentialPayload struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

func encodeCredentialPayload(payload credentialPayload) ([]byte, string, error) {
	data, err := json.Marshal(payload)
	return data, "", err
}

func decodeCredentialPayload(data []byte) (credentialPayload, error) {
	var payload credentialPayload
	if len(data) == 0 {
		return payload, nil
	}
	err := json.Unmarshal(data, &payload)
	return payload, err
}

func (d *Driver) debugExtra() map[string]any {
	source := d.tokenSource
	if source == "" {
		source = credentialSource(d.useOnlineAPI)
	}
	extra := map[string]any{drive.DebugExtraCredentialSource: source}
	d.cred.Debug(extra)
	return extra
}
