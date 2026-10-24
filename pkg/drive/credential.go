package drive

import (
	"context"
	"errors"
	"time"
)

// ErrCredentialVersionConflict reports that another mount committed a newer
// credential session before this writer did. The caller should read Snapshot
// again instead of overwriting the newer value.
var ErrCredentialVersionConflict = errors.New("drive: credential session version conflict")

// CredentialSnapshot is one immutable view of a shared credential session. The
// payload is opaque to qrypt: the driver owns its schema (a cookie, an access
// token plus expiry, and so on).
type CredentialSnapshot struct {
	Payload   []byte
	Version   uint64
	Account   string
	UpdatedAt time.Time
}

// CredentialSession is the session shared by every mount that references the
// same configured credential. Implementations must persist successful updates,
// reject stale writes, and notify sibling mounts.
type CredentialSession interface {
	// Name is the configured credential name.
	Name() string
	// Snapshot returns the current payload and version.
	Snapshot() CredentialSnapshot
	// Update commits payload as the new session when expected still matches
	// the current version. A mismatch returns ErrCredentialVersionConflict.
	// Account is diagnostic metadata (for example a provider account id); it
	// may be empty when the driver cannot derive one.
	Update(expected uint64, payload []byte, account string) (CredentialSnapshot, error)
	// Subscribe invokes fn after every successful commit, including commits
	// from other mounts. fn must not call back into the session. The returned
	// cancel function is safe to call more than once.
	Subscribe(fn func(CredentialSnapshot)) (cancel func())
	// Refresh runs fn at most once at a time across every mount sharing the
	// credential and commits its result. Concurrent callers receive the same
	// snapshot or error. A failed refresh must not modify the session.
	Refresh(ctx context.Context, fn func(current CredentialSnapshot) (payload []byte, account string, err error)) (CredentialSnapshot, error)
}

// CredentialSessionInstaller is implemented by drivers whose credential
// rotates or refreshes at runtime. The core installs the shared session before
// Init; drivers that do not implement it authenticate from static params only.
type CredentialSessionInstaller interface {
	InstallCredentialSession(session CredentialSession)
}

// CredentialExporter lets a driver adopt a legacy per-mount session into the
// shared credential store during an explicit migration. ok=false means the
// legacy state does not belong to the supplied credential params (a different
// account, for example), so the caller must not adopt it.
type CredentialExporter interface {
	ExportCredentialSession(store StateStore, params Params) (payload []byte, account string, ok bool, err error)
	LegacyCredentialFile() string
}

// AdoptLatestCredential applies the session payload when it advanced past
// lastVersion and reports whether the caller should retry the failed request.
// Drivers use it on ErrAuth so a rotation committed by a sibling mount recovers
// the request without a restart; when nothing newer exists the original error
// must surface so a genuine authentication failure is not hidden.
func AdoptLatestCredential(session CredentialSession, lastVersion uint64, apply func(payload []byte) error) (version uint64, adopted bool, err error) {
	if session == nil {
		return lastVersion, false, nil
	}
	snapshot := session.Snapshot()
	if snapshot.Version <= lastVersion || len(snapshot.Payload) == 0 {
		return lastVersion, false, nil
	}
	if err := apply(snapshot.Payload); err != nil {
		return lastVersion, false, err
	}
	return snapshot.Version, true, nil
}
