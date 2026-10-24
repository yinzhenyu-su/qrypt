package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/config"
)

// CredentialAdoption reports a legacy inline mount converted to a shared
// credential.
type CredentialAdoption struct {
	Credential   string
	Mount        string
	Account      string
	StateAdopted bool
	LegacyFile   string
	Reason       string
}

// AdoptMountCredential moves a mount's inline credential-scope parameters into
// a new credential, points the mount at it, and adopts the mount's legacy
// rotation state when the driver can prove it belongs to the same account.
func AdoptMountCredential(cfg *config.Config, layout RuntimeLayout, mountName, credentialName string, deleteLegacyState bool) (CredentialAdoption, error) {
	result := CredentialAdoption{Credential: credentialName, Mount: mountName}
	if err := cfg.AdoptMountCredential(mountName, credentialName); err != nil {
		return result, err
	}
	if err := config.Validate(cfg); err != nil {
		return result, err
	}
	migration, err := MigrateCredentialSession(cfg, layout, mountName, credentialName, deleteLegacyState)
	if err != nil {
		if errors.Is(err, ErrCredentialMigrationUnsupported) {
			return result, nil
		}
		return result, err
	}
	result.Account = migration.Account
	result.StateAdopted = migration.Adopted
	result.LegacyFile = migration.LegacyFile
	result.Reason = migration.Reason
	return result, nil
}

// PurgeCredentialState removes the persisted session directory of a credential.
// It refuses names that are not a single path component.
func PurgeCredentialState(layout RuntimeLayout, name string) error {
	if !singlePathComponent(name) {
		return fmt.Errorf("core: invalid credential name %q", name)
	}
	return removeStatePath(layout.StateDir, "credentials", name)
}

// PurgeMountState removes the per-mount driver state directory (upload
// sessions, offline layout, legacy credential files).
func PurgeMountState(layout RuntimeLayout, name string) error {
	if !singlePathComponent(name) {
		return fmt.Errorf("core: invalid mount name %q", name)
	}
	return removeStatePath(layout.DriverDir, name)
}

func singlePathComponent(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`) && filepath.Base(name) == name
}

func removeStatePath(base string, elems ...string) error {
	if base == "" {
		return fmt.Errorf("core: state directory is empty")
	}
	parts := append([]string{base}, elems...)
	if err := os.RemoveAll(filepath.Join(parts...)); err != nil {
		return fmt.Errorf("remove state %s: %w", filepath.Join(elems...), err)
	}
	return nil
}
