package config

import "fmt"

// MountByName returns the named mount definition.
func (c *Config) MountByName(name string) (MountConfig, bool) {
	if c == nil || name == "" {
		return MountConfig{}, false
	}
	for _, mount := range c.Mounts {
		if mount.Name == name {
			return mount, true
		}
	}
	return MountConfig{}, false
}

// UpsertMount adds or replaces a mount by name. Driver/scope validation and
// credential references are checked by Validate.
func (c *Config) UpsertMount(mount MountConfig) error {
	if c == nil {
		return fmt.Errorf("config: configuration is empty")
	}
	if mount.Name == "" {
		return fmt.Errorf("config: mount name is required")
	}
	for i := range c.Mounts {
		if c.Mounts[i].Name == mount.Name {
			c.Mounts[i] = mount
			return nil
		}
	}
	c.Mounts = append(c.Mounts, mount)
	return nil
}

// RemoveMount removes a mount that is not the configured upload default.
func (c *Config) RemoveMount(name string) error {
	if c == nil {
		return fmt.Errorf("config: configuration is empty")
	}
	if name == "" {
		return fmt.Errorf("config: mount name is required")
	}
	if c.Upload.DefaultMount == name {
		return fmt.Errorf("config: mount %q is the upload.default_mount; change that setting first", name)
	}
	if len(c.Mounts) == 1 {
		return fmt.Errorf("config: mount %q is the last mount; at least one mount is required, edit or reset the config instead", name)
	}
	for i := range c.Mounts {
		if c.Mounts[i].Name == name {
			c.Mounts = append(c.Mounts[:i], c.Mounts[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("config: mount %q not found", name)
}
