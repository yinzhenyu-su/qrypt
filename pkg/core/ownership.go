package core

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/buildinfo"
	"github.com/yinzhenyu/qrypt/pkg/core/ownerlock"
)

// OwnerOptions opts a filesystem-building entry point into the single-writer
// ownership contract: exactly one runtime may own a state root at a time.
//
// The lock is taken before the VFS is built and started, and released only
// after the filesystem has closed, so two runtimes can never interleave journal
// recovery, upload workers or read-cache index writes.
type OwnerOptions struct {
	// ConfigPath identifies the config file whose fingerprint must match the
	// running owner. It falls back to Options.ConfigPath.
	ConfigPath string
	// Fingerprint overrides the config-file hash for programmatic callers
	// that have no config file (tests, embedding). Leave empty to hash
	// ConfigPath.
	Fingerprint string
}

// WritableDirectories lists the directories a runtime writes to. It is the
// input to the ownership lock's overlap validation and mirrors the layout
// produced by NewStorageLayout.
func WritableDirectories(layout RuntimeLayout) []ownerlock.Dir {
	return []ownerlock.Dir{
		{Label: "run dir", Path: layout.RunDir},
		{Label: "read cache", Path: layout.ReadCacheDir},
		{Label: "thumbnail cache", Path: layout.ThumbnailDir},
		{Label: "upload dir", Path: layout.UploadDir},
		{Label: "state dir", Path: layout.StateDir},
		{Label: "log dir", Path: layout.LogDir},
		{Label: "tmp dir", Path: layout.TmpDir},
	}
}

// acquireOwnerLock takes the state-root ownership lock when the caller opted
// in. The returned release function must run after the filesystem closes.
func acquireOwnerLock(opts Options, layout RuntimeLayout) (func(), error) {
	if opts.Owner == nil {
		return func() {}, nil
	}
	configPath := opts.Owner.ConfigPath
	if configPath == "" {
		configPath = opts.ConfigPath
	}
	if configPath != "" {
		absolute, err := filepath.Abs(configPath)
		if err != nil {
			return nil, err
		}
		configPath = absolute
	}
	fingerprint := opts.Owner.Fingerprint
	if fingerprint == "" {
		if configPath == "" {
			return nil, fmt.Errorf("core: owner lock requires a config path or an explicit fingerprint")
		}
		computed, err := ownerlock.FingerprintFile(configPath)
		if err != nil {
			return nil, err
		}
		fingerprint = computed
	}
	lease, err := ownerlock.Acquire(layout.RootDir, ownerlock.Meta{
		PID:          os.Getpid(),
		QryptVersion: buildinfo.Current().Version,
		ConfigPath:   configPath,
		Fingerprint:  fingerprint,
		StartedAt:    time.Now().UTC(),
	}, WritableDirectories(layout))
	if err != nil {
		return nil, fmt.Errorf("core: %w", err)
	}
	return func() { _ = lease.Release() }, nil
}
