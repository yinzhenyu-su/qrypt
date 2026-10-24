package credential

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type loginTarget struct {
	name            string
	overrideUnknown bool
}

func resolveLoginCredential(cfg *config.Config, layout core.RuntimeLayout, provider drive.LoginProvider, providerType, account string, cmd *cobra.Command) (loginTarget, error) {
	requested, _ := cmd.Flags().GetString("name")
	requested = strings.TrimSpace(requested)
	if cfg == nil {
		if requested == "" {
			return loginTarget{name: derivedCredentialName(providerType, cfg)}, nil
		}
		return loginTarget{name: requested}, nil
	}

	if requested != "" {
		cred, exists := cfg.CredentialFor(requested)
		if !exists {
			return loginTarget{name: requested}, nil
		}
		suggestion := derivedCredentialName(providerType, cfg)
		if cred.Type != providerType {
			return loginTarget{}, fmt.Errorf("credential %q has type %q, not %q; choose another --name, for example:\n  qrypt credential login --type %s --name %s",
				requested, cred.Type, providerType, providerType, suggestion)
		}
		info := core.ResolveCredentialAccount(cfg, layout, requested, provider)
		if info.Account != "" && account != "" && info.Account != account {
			return loginTarget{}, fmt.Errorf("credential %q belongs to account %s, not the authenticated account %s; choose another --name, for example:\n  qrypt credential login --type %s --name %s",
				requested, info.Account, account, providerType, suggestion)
		}
		return loginTarget{name: requested, overrideUnknown: info.Account == ""}, nil
	}

	if account != "" {
		for _, cred := range cfg.Credentials {
			if cred.Type != providerType {
				continue
			}
			info := core.ResolveCredentialAccount(cfg, layout, cred.Name, provider)
			if info.Account == account {
				return loginTarget{name: cred.Name}, nil
			}
		}
	}
	return loginTarget{name: derivedCredentialName(providerType, cfg)}, nil
}
