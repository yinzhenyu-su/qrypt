package mount

import (
	"sort"
	"strings"
)

func (a *adapter) setXattr(path, name string, value []byte) {
	key := cleanMountPath(path)
	copied := append([]byte(nil), value...)
	a.mu.Lock()
	if a.xattrs[key] == nil {
		a.xattrs[key] = map[string][]byte{}
	}
	a.xattrs[key][name] = copied
	a.mu.Unlock()
}

func (a *adapter) getXattr(path, name string) ([]byte, bool) {
	key := cleanMountPath(path)
	a.mu.Lock()
	value, ok := a.xattrs[key][name]
	a.mu.Unlock()
	if !ok {
		return nil, false
	}
	return append([]byte(nil), value...), true
}

func (a *adapter) listXattrs(path string) []string {
	key := cleanMountPath(path)
	a.mu.Lock()
	names := make([]string, 0, len(a.xattrs[key]))
	for name := range a.xattrs[key] {
		names = append(names, name)
	}
	a.mu.Unlock()
	sort.Strings(names)
	return names
}

func (a *adapter) removeXattr(path, name string) {
	key := cleanMountPath(path)
	a.mu.Lock()
	if attrs := a.xattrs[key]; attrs != nil {
		delete(attrs, name)
		if len(attrs) == 0 {
			delete(a.xattrs, key)
		}
	}
	a.mu.Unlock()
}

func (a *adapter) removeXattrs(path string) {
	key := cleanMountPath(path)
	a.mu.Lock()
	for existing := range a.xattrs {
		if existing == key || strings.HasPrefix(existing, key+"/") {
			delete(a.xattrs, existing)
		}
	}
	a.mu.Unlock()
}

func (a *adapter) renameXattrs(oldPath, newPath string) {
	oldKey := cleanMountPath(oldPath)
	newKey := cleanMountPath(newPath)
	a.mu.Lock()
	for existing, attrs := range a.xattrs {
		if existing != oldKey && !strings.HasPrefix(existing, oldKey+"/") {
			continue
		}
		next := newKey + strings.TrimPrefix(existing, oldKey)
		delete(a.xattrs, existing)
		a.xattrs[next] = attrs
	}
	a.mu.Unlock()
}
