package credential

import (
	"fmt"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/config"
)

func derivedCredentialName(providerType string, cfg *config.Config) string {
	return nextAvailableName(providerType, func(name string) bool {
		return credentialExists(cfg, name)
	})
}

// nextAvailableName assigns base, base-2, base-3, ... until taken reports false.
// Mount and credential names use different collision rules, so the caller owns
// the predicate.
func nextAvailableName(base string, taken func(string) bool) string {
	base = sanitizeNameComponent(base)
	candidate := base
	for index := 2; ; index++ {
		if !taken(candidate) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, index)
	}
}

func credentialExists(cfg *config.Config, name string) bool {
	if cfg == nil {
		return false
	}
	_, ok := cfg.CredentialFor(name)
	return ok
}

func sanitizeNameComponent(value string) string {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("/", "-", "\\", "-").Replace(value)
	if value == "" || value == "." || value == ".." {
		return "mount"
	}
	return value
}
