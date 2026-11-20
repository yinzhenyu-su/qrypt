package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/vfs/listcache"
)

// listCacheSettings resolves the persisted list cache for one mount:
// [list_cache] defaults overridden by [mounts.list_cache]. An explicit
// ttl "0" disables the cache for the mount.
func listCacheSettings(cfg *config.Config, mountCfg config.MountConfig) (time.Duration, int64, error) {
	cache := cfg.ListCacheFor(mountCfg.Name)
	ttl := listcache.DefaultTTL
	if cache.TTL != "" {
		parsed, err := config.ParseDuration(cache.TTL)
		if err != nil {
			return 0, 0, fmt.Errorf("config: mount %s invalid list_cache.ttl: %w", mountCfg.Name, err)
		}
		ttl = parsed
	}
	maxBytes := listcache.DefaultMaxBytes
	if cache.MaxSize != "" {
		parsed, err := config.ParseSize(cache.MaxSize)
		if err != nil {
			return 0, 0, fmt.Errorf("config: mount %s invalid list_cache.max_size: %w", mountCfg.Name, err)
		}
		maxBytes = parsed
	}
	return ttl, maxBytes, nil
}

// listCacheIdentity fingerprints everything that changes what a directory
// listing means: the driver type, resolved backend root, mount parameters,
// encryption config and the credential entry. Listings written under another
// identity (account switch, encoder change, different server with the same
// root path) are discarded instead of being served.
func listCacheIdentity(cfg *config.Config, mountCfg config.MountConfig, rootID string, encryption any) string {
	payload := struct {
		Version    int
		Type       string
		RootID     string
		Params     config.ParamMap
		Encryption any
		Credential *config.CredentialConfig
	}{
		Version:    1,
		Type:       mountCfg.Type,
		RootID:     rootID,
		Params:     mountCfg.Params,
		Encryption: encryption,
	}
	if mountCfg.Credential != "" {
		if cred, ok := cfg.CredentialFor(mountCfg.Credential); ok {
			payload.Credential = &cred
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		// Marshaling plain config data cannot fail in practice; fall back to
		// a weaker but stable fingerprint rather than disabling the cache.
		raw = []byte(fmt.Sprintf("%d/%s/%s", payload.Version, payload.Type, payload.RootID))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
