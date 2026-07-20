package cli

import "github.com/yinzhenyu/qrypt/internal/config"

func validateConfig(cfg *config.Config) error {
	return config.Validate(cfg)
}
