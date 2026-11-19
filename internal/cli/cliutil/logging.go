package cliutil

import (
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// InitLogging applies cfg's logging settings to the process logger. Commands
// call it after the config load, and runtime rewrites (such as an isolated
// measurement mount changing the level) call it again to take effect.
func InitLogging(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	level := cfg.Logging.LogLevel
	logFile := util.ExpandHome(cfg.EffectiveLogFile())
	errFile := util.ExpandHome(cfg.EffectiveErrorFile())
	if level == "" && logFile == "" && errFile == "" {
		return nil
	}
	if level == "" {
		level = "info"
	}
	newLogger, err := logging.New(level, logFile, errFile, nil)
	if err != nil {
		return fmt.Errorf("initialize logging: %w", err)
	}
	logging.ReplaceDefault(newLogger)
	return nil
}
