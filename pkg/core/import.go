package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const ImportedConfigName = "qrypt.toml"

type FileInfo struct {
	Path    string `json:"path"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time,omitempty"`
}

type ResumeCheck struct {
	OK      bool     `json:"ok"`
	Current FileInfo `json:"current"`
	Reason  string   `json:"reason,omitempty"`
}

type LogFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func ImportConfig(srcPath string, runtime RuntimeLayout) (string, error) {
	if srcPath == "" {
		return "", fmt.Errorf("core: source config path required")
	}
	if runtime.ConfigDir == "" {
		return "", fmt.Errorf("core: runtime config dir required")
	}
	layout := NewStorageLayout(nil, runtime)
	if err := ensureRuntimeLayout(layout); err != nil {
		return "", err
	}
	cfg, err := config.Load(srcPath)
	if err != nil {
		return "", err
	}
	sanitizeImportedConfig(cfg)
	dstPath := filepath.Join(layout.ConfigDir, ImportedConfigName)
	if err := config.Save(dstPath, cfg); err != nil {
		return "", err
	}
	return dstPath, nil
}

func ImportedConfigPath(runtime RuntimeLayout) (string, error) {
	if runtime.ConfigDir == "" {
		return "", fmt.Errorf("core: runtime config dir required")
	}
	return filepath.Join(runtime.ConfigDir, ImportedConfigName), nil
}

func OpenImported(ctx context.Context, runtime RuntimeLayout) (*Core, error) {
	path, err := ImportedConfigPath(runtime)
	if err != nil {
		return nil, err
	}
	return Open(ctx, Options{ConfigPath: path, Runtime: runtime})
}

func sanitizeImportedConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.MountPoint = ""
	cfg.Storage = config.StorageConfig{}
	cfg.Logging.LogFile = ""
	cfg.Logging.ErrorFile = ""
}

func (c *Core) FileInfo(ctx context.Context, path string) (FileInfo, error) {
	entry, err := c.Stat(ctx, path)
	if err != nil {
		return FileInfo{}, err
	}
	return fileInfoFromEntry(path, entry), nil
}

func (c *Core) ValidateResume(ctx context.Context, path, id string, size int64, modTime string) (ResumeCheck, error) {
	info, err := c.FileInfo(ctx, path)
	if err != nil {
		return ResumeCheck{}, err
	}
	check := ResumeCheck{OK: true, Current: info}
	switch {
	case id != "" && info.ID != id:
		check.OK = false
		check.Reason = "id_changed"
	case size >= 0 && info.Size != size:
		check.OK = false
		check.Reason = "size_changed"
	case modTime != "" && info.ModTime != "" && info.ModTime != modTime:
		check.OK = false
		check.Reason = "mod_time_changed"
	}
	return check, nil
}

func (c *Core) LogFiles() ([]LogFile, error) {
	if c == nil || c.runtimeLayout.LogDir == "" {
		return nil, fmt.Errorf("core: logs unavailable")
	}
	entries, err := os.ReadDir(c.runtimeLayout.LogDir)
	if err != nil {
		return nil, err
	}
	var files []LogFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, LogFile{Name: entry.Name(), Size: info.Size()})
	}
	return files, nil
}

func (c *Core) ReadLog(name string, offset int64, length int) ([]byte, error) {
	if c == nil || c.runtimeLayout.LogDir == "" {
		return nil, fmt.Errorf("core: logs unavailable")
	}
	if offset < 0 {
		return nil, fmt.Errorf("core: log offset must be non-negative")
	}
	if length < 0 {
		return nil, fmt.Errorf("core: log length must be non-negative")
	}
	if length == 0 {
		return []byte{}, nil
	}
	if length > c.readLimit(0) {
		return nil, fmt.Errorf("core: log length %d exceeds limit %d", length, c.readLimit(0))
	}
	path := filepath.Join(c.runtimeLayout.LogDir, filepath.Base(filepath.Clean(name)))
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, length)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

func fileInfoFromEntry(path string, entry drive.Entry) FileInfo {
	info := FileInfo{
		Path:  path,
		ID:    entry.ID,
		Name:  entry.Name,
		IsDir: entry.IsDir,
		Size:  entry.Size,
	}
	if !entry.ModTime.IsZero() {
		info.ModTime = entry.ModTime.Format(timeFormatRFC3339)
	}
	return info
}

const timeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"
