package core

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/core/internal/credentials"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestCredential authenticates a credential without mounting anything. Extra
// params cover mount-scope requirements of the driver (for example a bucket or
// a root_path); the credential's own params are the base.
func TestCredential(ctx context.Context, cfg *config.Config, layout RuntimeLayout, name string, overrides map[string]string, timeout time.Duration) error {
	cred, ok := cfg.CredentialFor(name)
	if !ok {
		return fmt.Errorf("config: credential %q not found", name)
	}
	params := drive.Params{}
	for key, value := range cred.Params {
		params[key] = value
	}
	for key, value := range overrides {
		params[key] = value
	}
	driver, err := drive.New(cred.Type, params)
	if err != nil {
		return fmt.Errorf("credential %q: %w", name, err)
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stateDir, err := os.MkdirTemp("", "qrypt-credential-test-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stateDir)
	if installer, ok := driver.(drive.StateStoreInstaller); ok {
		installer.InstallStateStore(drive.NewFileStateStore(stateDir))
	}
	if installer, ok := driver.(drive.CredentialSessionInstaller); ok {
		installer.InstallCredentialSession(credentials.Open(layout.StateDir).Session(credentials.Spec{
			Name:       cred.Name,
			ParamsSeed: credentials.ParamsSeed(cred.Type, cred.Params),
		}))
	}
	if err := driver.Init(runCtx); err != nil {
		return fmt.Errorf("credential %q: %w", name, err)
	}
	_ = driver.Drop(runCtx)
	return nil
}
