package driverutil

import "github.com/yinzhenyu/qrypt/pkg/drive"

// AppendCredentialDebug adds shared-credential identity to a debug snapshot
// without exposing any secret value.
func AppendCredentialDebug(extra map[string]any, session drive.CredentialSession) {
	if session == nil {
		return
	}
	snapshot := session.Snapshot()
	extra[drive.DebugExtraCredentialName] = session.Name()
	extra[drive.DebugExtraCredentialVersion] = snapshot.Version
	extra[drive.DebugExtraCredentialAccount] = snapshot.Account
}
