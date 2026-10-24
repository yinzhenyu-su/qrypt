package onedrive

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

func (d *Driver) loadCredentialSession() {
	snapshot := d.credential.Snapshot()
	payload, err := decodeCredentialPayload(snapshot.Payload)
	if err != nil {
		logging.L.Warnf("[onedrive] decode shared credential session: %v", err)
		return
	}
	if payload.AccessToken == "" && payload.RefreshToken == "" {
		return
	}
	d.setTokens(payload.AccessToken, payload.RefreshToken)
	d.credentialVersion = snapshot.Version
	d.tokenSource = "state"
}

func (d *Driver) subscribeCredentialSession() {
	if d.credential == nil || d.credentialCancel != nil {
		return
	}
	d.credentialCancel = d.credential.Subscribe(d.applyCredentialSnapshot)
}

func (d *Driver) applyCredentialSnapshot(snapshot drive.CredentialSnapshot) {
	payload, err := decodeCredentialPayload(snapshot.Payload)
	if err != nil {
		logging.L.Warnf("[onedrive] decode shared credential notification: %v", err)
		return
	}
	if payload.AccessToken == "" && payload.RefreshToken == "" {
		return
	}
	d.credentialMu.Lock()
	if snapshot.Version <= d.credentialVersion {
		d.credentialMu.Unlock()
		return
	}
	d.credentialVersion = snapshot.Version
	d.tokenSource = "credential"
	d.credentialMu.Unlock()
	d.setTokens(payload.AccessToken, payload.RefreshToken)
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
	driverutil.AppendCredentialDebug(extra, d.credential)
	return extra
}
