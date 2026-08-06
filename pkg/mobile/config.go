package mobile

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/core"
)

// ConfigSummaryJSON returns a settings-UI friendly view of the current
// config file: mounts (with secret params masked), read cache, thumbnail
// cache, and upload settings.
func ConfigSummaryJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	raw, err := withCore(s, func(c *core.Core) (string, error) { return c.ConfigSummaryJSON() })
	return rawResultJSON(raw, err)
}

// UpdateConfigJSON applies mount changes (add/update/remove) and optional
// global settings to the config file. Changes are validated before saving;
// a failed update leaves the previous config untouched. The returned summary
// reflects the saved config. Changes take effect on the next core open or
// after ReloadConfigJSON.
func UpdateConfigJSON(coreID, updateRaw string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var req core.ConfigUpdateRequest
	if err := json.Unmarshal([]byte(updateRaw), &req); err != nil {
		return resultJSON(nil, wrapError(err))
	}
	summary, err := withCore(s, func(c *core.Core) (core.ConfigSummary, error) { return c.ApplyConfigUpdate(req) })
	return resultJSON(summary, err)
}

// ReloadConfigJSON reopens the core from the current config file with the
// same runtime layout. All handles for the session are invalidated; the app
// must re-open file/task/event handles afterwards. The session id stays the
// same. If the config fails to load, the previous core keeps running and an
// error is returned.
func ReloadConfigJSON(coreID string, deadlineMS int) string {
	registry.mu.Lock()
	s, ok := registry.sessions[coreID]
	if !ok {
		registry.mu.Unlock()
		return resultJSON(nil, wrapError(fmt.Errorf("mobile: unknown core %q", coreID)))
	}
	configPath := s.configPath
	runtime := s.runtime
	registry.mu.Unlock()

	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	newCore, err := core.Open(ctx, core.Options{ConfigPath: configPath, Runtime: runtime})
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}

	registry.mu.Lock()
	current, ok := registry.sessions[coreID]
	if !ok {
		registry.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), coreCloseTimeout)
		_ = newCore.Close(ctx)
		cancel()
		return resultJSON(nil, wrapError(fmt.Errorf("mobile: session %q disappeared during reload", coreID)))
	}
	// Hold the session write lock while swapping the core so no in-flight API
	// call is using the old core, then close it once every reader is done.
	current.mu.Lock()
	oldCore := current.core
	current.core = newCore
	handles := collectCoreHandlesLocked(coreID)
	current.mu.Unlock()
	registry.mu.Unlock()
	closeCollectedHandles(handles)
	// The old core may still be draining workers; give shutdown a bounded
	// window instead of an unbounded background close.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), coreCloseTimeout)
	defer closeCancel()
	_ = oldCore.Close(closeCtx)
	return resultJSON(true, nil)
}
