package core

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	// CredentialAccountSourceConfig means the account was derived from the
	// credential's static parameters; no runtime session was available.
	CredentialAccountSourceConfig = "config"
	// CredentialAccountSourceSession means the account metadata came from the
	// persisted shared session.
	CredentialAccountSourceSession = "state"
)

// CredentialAccountInfo is the offline identity and display metadata of one
// credential. Identity and session metadata are independent: Account may come
// from static parameters while Source/Version/UpdatedAt describe a session.
type CredentialAccountInfo struct {
	Account   string
	Source    string
	Version   uint64
	UpdatedAt time.Time
}

// ResolveCredentialAccount reports the account a credential represents without
// contacting the network.
//
// Identity is resolved from static parameters first (provider.Account) and
// falls back to the persisted session account. Display metadata is resolved the
// other way around: an existing session always supplies Source, Version, and
// UpdatedAt, even when the account itself came from static parameters.
//
// provider may be nil for drivers that do not implement interactive login; such
// credentials still resolve from their session, or report unknown.
func ResolveCredentialAccount(cfg *config.Config, layout RuntimeLayout, name string, provider drive.LoginProvider) CredentialAccountInfo {
	if cfg == nil {
		return CredentialAccountInfo{}
	}
	cred, ok := cfg.CredentialFor(name)
	if !ok {
		return CredentialAccountInfo{}
	}

	account := ""
	known := false
	if provider != nil {
		account, known = provider.Account(drive.Params(cred.Params))
	}

	// Read the session even when the static parameters identified the account:
	// the session is the only source of source/version/updated metadata.
	session, hasSession := CredentialSessionState(cfg, layout, name)
	if !known && hasSession {
		account, known = session.Account, session.Account != ""
	}

	info := CredentialAccountInfo{Account: account}
	switch {
	case hasSession:
		info.Source = CredentialAccountSourceSession
		info.Version = session.Version
		info.UpdatedAt = session.UpdatedAt
	case known:
		info.Source = CredentialAccountSourceConfig
	}
	return info
}
