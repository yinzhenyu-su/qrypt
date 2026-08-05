package quark

import (
	"context"
	"fmt"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) resolvePathFrom(ctx context.Context, rootID, path string) (string, error) {
	currentID := d.resolve(rootID)
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if segment == "" {
			continue
		}
		entries, err := d.List(ctx, currentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.Name == segment {
				currentID = entry.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%w: quark: child not found: %s", drive.ErrNotFound, segment)
		}
	}
	return currentID, nil
}
