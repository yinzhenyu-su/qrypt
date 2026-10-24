package onedrive

import "github.com/yinzhenyu/qrypt/pkg/drive"

// OneDrive keeps tokens in memory only, so there is no legacy file to migrate.
// The methods still satisfy drive.CredentialExporter so the shared session is
// used when the credential is configured.
func (d *Driver) LegacyCredentialFile() string { return "" }

func (d *Driver) ExportCredentialSession(drive.StateStore, drive.Params) ([]byte, string, bool, error) {
	return nil, "", false, nil
}
