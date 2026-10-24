package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/core/internal/credentials"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ErrCredentialMigrationUnsupported marks drivers that keep no exportable
// legacy session; callers may treat it as "nothing to migrate".
var ErrCredentialMigrationUnsupported = errors.New("core: credential migration unsupported")

// CredentialMigration reports the outcome of moving one mount's legacy
// per-mount session into a shared credential.
type CredentialMigration struct {
	Credential string
	Account    string
	Adopted    bool
	LegacyFile string
}

// MigrateCredentialSession adopts a mount's legacy rotation state into the
// shared credential store. The driver decides whether the legacy state belongs
// to the credential params; a mismatch is reported as a non-adoption rather than
// an error. The legacy file is only removed when deleteLegacy is true.
func MigrateCredentialSession(cfg *config.Config, layout RuntimeLayout, mountName, credentialName string, deleteLegacy bool) (CredentialMigration, error) {
	result := CredentialMigration{Credential: credentialName}
	mount, ok := findMount(cfg, mountName)
	if !ok {
		return result, fmt.Errorf("config: mount %q not found", mountName)
	}
	if mount.Credential != credentialName {
		return result, fmt.Errorf("config: mount %q does not reference credential %q", mountName, credentialName)
	}
	cred, ok := cfg.CredentialFor(credentialName)
	if !ok {
		return result, fmt.Errorf("config: credential %q not found", credentialName)
	}
	if cred.Type != mount.Type {
		return result, fmt.Errorf("config: mount %q type %q does not match credential %q type %q", mountName, mount.Type, credentialName, cred.Type)
	}
	params, err := cfg.ResolveMountParams(mount)
	if err != nil {
		return result, err
	}
	driver, err := drive.New(mount.Type, params)
	if err != nil {
		return result, err
	}
	exporter, ok := driver.(drive.CredentialExporter)
	if !ok {
		return result, fmt.Errorf("%w: driver %q", ErrCredentialMigrationUnsupported, mount.Type)
	}
	legacy := drive.NewFileStateStore(filepath.Join(layout.DriverDir, mountName))
	payload, account, adoptable, err := exporter.ExportCredentialSession(legacy, params)
	if err != nil {
		if drive.IsStateNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("read legacy credential state: %w", err)
	}
	if !adoptable {
		return result, nil
	}
	manager := credentials.Open(layout.StateDir)
	session := manager.Session(credentials.Spec{
		Name:       cred.Name,
		ParamsSeed: credentials.ParamsSeed(cred.Type, cred.Params),
	})
	snapshot := session.Snapshot()
	if len(snapshot.Payload) > 0 {
		return result, fmt.Errorf("credential %q already has a session", credentialName)
	}
	if _, err := session.Update(snapshot.Version, payload, account); err != nil {
		return result, fmt.Errorf("store credential session: %w", err)
	}
	result.Adopted = true
	result.Account = account
	result.LegacyFile = exporter.LegacyCredentialFile()
	if deleteLegacy {
		_ = os.Remove(filepath.Join(layout.DriverDir, mountName, result.LegacyFile))
	}
	return result, nil
}

// CredentialSessionInfo is the observable state of a credential's shared
// session. Secrets never appear here.
type CredentialSessionInfo struct {
	Account   string    `json:"account,omitempty"`
	Version   uint64    `json:"version"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// CredentialSessionState reads the persisted session for a credential without
// opening any mount. It is used by `qrypt credential list/show` so the account
// and rotation state are visible even when no Core is running.
func CredentialSessionState(cfg *config.Config, layout RuntimeLayout, name string) (CredentialSessionInfo, bool) {
	cred, ok := cfg.CredentialFor(name)
	if !ok {
		return CredentialSessionInfo{}, false
	}
	manager := credentials.Open(layout.StateDir)
	session := manager.Session(credentials.Spec{
		Name:       cred.Name,
		ParamsSeed: credentials.ParamsSeed(cred.Type, cred.Params),
	})
	snapshot := session.Snapshot()
	if snapshot.Version == 0 && snapshot.Account == "" && len(snapshot.Payload) == 0 {
		return CredentialSessionInfo{}, false
	}
	return CredentialSessionInfo{
		Account:   snapshot.Account,
		Version:   snapshot.Version,
		UpdatedAt: snapshot.UpdatedAt,
	}, true
}

func findMount(cfg *config.Config, name string) (config.MountConfig, bool) {
	if cfg == nil {
		return config.MountConfig{}, false
	}
	for _, mount := range cfg.Mounts {
		if mount.Name == name {
			return mount, true
		}
	}
	return config.MountConfig{}, false
}
