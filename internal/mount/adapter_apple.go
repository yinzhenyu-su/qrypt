package mount

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (a *adapter) shouldIgnoreAppleMetadata(path string) bool {
	return a.ignoreAppleMetadata && (isAppleMetadataPath(path) || a.hasIgnoredApple(path))
}

func (a *adapter) hasIgnoredAppleMetadata(path string) bool {
	return a.ignoreAppleMetadata && a.hasIgnoredApple(path)
}

func (a *adapter) shouldIgnoreAppleXattr(name string) bool {
	return a.ignoreAppleXattr && strings.HasPrefix(strings.ToLower(name), "com.apple.")
}

func (a *adapter) hasIgnoredApple(path string) bool {
	key := cleanMountPath(path)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.ignoredApple[key]; ok {
		return true
	}
	for existing, node := range a.ignoredApple {
		if node.isDir && strings.HasPrefix(key, existing+"/") {
			return true
		}
	}
	return false
}

func (a *adapter) ensureIgnoredApple(path string, isDir bool) ignoredAppleNode {
	key := cleanMountPath(path)
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	node, ok := a.ignoredApple[key]
	if !ok {
		node = ignoredAppleNode{isDir: isDir, mtime: now}
	} else {
		node.isDir = node.isDir || isDir
		node.mtime = now
	}
	a.ignoredApple[key] = node
	return node
}

func (a *adapter) ensureIgnoredAppleParent(ctx context.Context, path string) error {
	parent := filepath.Dir(cleanMountPath(path))
	if parent == "." || parent == "/" || isAppleMetadataPath(parent) {
		return nil
	}
	if entry, err := a.fs.Stat(ctx, parent); err == nil && entry.IsDir {
		return nil
	}
	_, err := a.fs.Mkdir(ctx, parent)
	return err
}

func (a *adapter) ignoredAppleEntry(path string) drive.Entry {
	key := cleanMountPath(path)
	a.mu.Lock()
	node, ok := a.ignoredApple[key]
	a.mu.Unlock()
	if !ok {
		node = ignoredAppleNode{isDir: isAppleMetadataDirPath(path), mtime: time.Now()}
	}
	return drive.Entry{
		ID:      "ignored-apple-metadata:" + key,
		Name:    filepath.Base(key),
		Size:    node.size,
		IsDir:   node.isDir,
		ModTime: node.mtime,
	}
}

const ignoredAppleMaxInMemoryBytes = 16 << 20

func (a *adapter) writeIgnoredApple(path string, data []byte, off int64) {
	if off < 0 {
		off = 0
	}
	key := cleanMountPath(path)
	now := time.Now()
	a.mu.Lock()
	node := a.ignoredApple[key]
	node.isDir = false
	if end := off + int64(len(data)); end > node.size {
		node.size = end
	}
	if off < ignoredAppleMaxInMemoryBytes {
		end := off + int64(len(data))
		storeEnd := end
		if storeEnd > ignoredAppleMaxInMemoryBytes {
			storeEnd = ignoredAppleMaxInMemoryBytes
		}
		if storeEnd > int64(len(node.data)) {
			next := make([]byte, storeEnd)
			copy(next, node.data)
			node.data = next
		}
		copy(node.data[off:storeEnd], data[:storeEnd-off])
	}
	node.mtime = now
	a.ignoredApple[key] = node
	a.mu.Unlock()
}

func (a *adapter) readIgnoredApple(path string, buff []byte, off int64) int {
	if off < 0 {
		return 0
	}
	key := cleanMountPath(path)
	a.mu.Lock()
	node := a.ignoredApple[key]
	a.mu.Unlock()
	if node.isDir || off >= node.size {
		return 0
	}
	remaining := node.size - off
	if remaining > int64(len(buff)) {
		remaining = int64(len(buff))
	}
	n := int(remaining)
	clear(buff[:n])
	if off < int64(len(node.data)) {
		copied := copy(buff[:n], node.data[off:])
		clear(buff[copied:n])
	}
	return n
}

func (a *adapter) truncateIgnoredApple(path string, size int64) {
	if size < 0 {
		size = 0
	}
	key := cleanMountPath(path)
	now := time.Now()
	a.mu.Lock()
	node := a.ignoredApple[key]
	node.isDir = false
	node.size = size
	if size <= int64(len(node.data)) {
		node.data = node.data[:size]
	} else if size <= ignoredAppleMaxInMemoryBytes {
		next := make([]byte, size)
		copy(next, node.data)
		node.data = next
	}
	node.mtime = now
	a.ignoredApple[key] = node
	a.mu.Unlock()
}

func (a *adapter) removeIgnoredApple(path string) {
	key := cleanMountPath(path)
	a.mu.Lock()
	for existing := range a.ignoredApple {
		if existing == key || strings.HasPrefix(existing, key+"/") {
			delete(a.ignoredApple, existing)
		}
	}
	a.mu.Unlock()
}

func (a *adapter) renameIgnoredApple(oldPath, newPath string) {
	oldKey := cleanMountPath(oldPath)
	newKey := cleanMountPath(newPath)
	now := time.Now()
	a.mu.Lock()
	for existing, node := range a.ignoredApple {
		if existing != oldKey && !strings.HasPrefix(existing, oldKey+"/") {
			continue
		}
		next := newKey + strings.TrimPrefix(existing, oldKey)
		delete(a.ignoredApple, existing)
		node.mtime = now
		a.ignoredApple[next] = node
	}
	if _, ok := a.ignoredApple[newKey]; !ok {
		a.ignoredApple[newKey] = ignoredAppleNode{isDir: isAppleMetadataDirPath(newPath), mtime: now}
	}
	a.mu.Unlock()
}

func isAppleMetadataPath(path string) bool {
	segments := strings.Split(cleanMountPath(path), "/")
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		if isAppleMetadataName(segment) || isAppleMetadataDirName(segment) {
			return true
		}
	}
	return false
}

func isAppleMetadataDirPath(path string) bool {
	for _, segment := range strings.Split(cleanMountPath(path), "/") {
		if isAppleMetadataDirName(segment) {
			return true
		}
	}
	return false
}

func isAppleMetadataName(name string) bool {
	return name == ".DS_Store" ||
		name == ".VolumeIcon.icns" ||
		name == ".metadata_never_index" ||
		name == ".com.apple.timemachine.donotpresent" ||
		strings.HasPrefix(name, "._")
}

func isAppleMetadataDirName(name string) bool {
	switch name {
	case ".Spotlight-V100", ".Trashes", ".fseventsd", ".TemporaryItems", ".DocumentRevisions-V100", "__MACOSX":
		return true
	default:
		return false
	}
}

func cleanMountPath(path string) string {
	return filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(path, "/")))
}
