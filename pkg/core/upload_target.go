package core

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

type uploadDestination struct {
	Path       string
	DefaultDir string
}

type UploadDestinationResolver struct {
	DefaultMount string
	DefaultPath  string
}

func NewUploadDestinationResolver(defaultMount, defaultPath string) UploadDestinationResolver {
	return UploadDestinationResolver{DefaultMount: defaultMount, DefaultPath: defaultPath}
}

func (c *Core) resolveUploadDestPath(ctx context.Context, destPath, fallbackName string) (string, error) {
	service, err := c.UploadService()
	if err != nil {
		return "", err
	}
	return service.ResolveDestination(ctx, destPath, fallbackName)
}

func (c *Core) ResolveUploadDestination(destPath, fallbackName string) (string, error) {
	service, err := c.UploadService()
	if err != nil {
		return "", err
	}
	return service.PreviewDestination(destPath, fallbackName)
}

func (r UploadDestinationResolver) Resolve(destPath, fallbackName string) (uploadDestination, error) {
	destPath = strings.TrimSpace(destPath)
	if strings.HasPrefix(destPath, "/") {
		return uploadDestination{Path: vfs.CleanVirtualPath(destPath)}, nil
	}
	if destPath == "" {
		destPath = strings.Trim(strings.TrimSpace(fallbackName), "/")
	}
	if destPath == "" || destPath == "." {
		return uploadDestination{}, fmt.Errorf("core: upload destination required")
	}
	if strings.TrimSpace(r.DefaultMount) == "" {
		return uploadDestination{}, fmt.Errorf("core: upload destination must be absolute when upload.default_mount is not configured")
	}
	base := "/"
	if strings.TrimSpace(r.DefaultPath) != "" {
		base = r.DefaultPath
	}
	defaultDir := vfs.CleanVirtualPath(path.Join("/", r.DefaultMount, base))
	if defaultDir == "/"+strings.Trim(r.DefaultMount, "/") {
		defaultDir = ""
	}
	return uploadDestination{
		Path:       vfs.CleanVirtualPath(path.Join(defaultDirOrMount(r.DefaultMount, defaultDir), destPath)),
		DefaultDir: defaultDir,
	}, nil
}

func defaultDirOrMount(mount, defaultDir string) string {
	if defaultDir != "" {
		return defaultDir
	}
	return vfs.CleanVirtualPath(path.Join("/", mount))
}
