package onedrive

import (
	"encoding/json"

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
