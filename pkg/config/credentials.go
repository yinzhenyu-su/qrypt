package config

import (
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// CredentialUsedBy returns the mounts that reference the named credential.
func (c *Config) CredentialUsedBy(name string) []string {
	if c == nil || name == "" {
		return nil
	}
	var mounts []string
	for _, mount := range c.Mounts {
		if mount.Credential == name {
			mounts = append(mounts, mount.Name)
		}
	}
	return mounts
}

// UpsertCredential adds or replaces a credential by name. Parameter and scope
// validation is performed by Validate.
func (c *Config) UpsertCredential(cred CredentialConfig) error {
	if c == nil {
		return fmt.Errorf("config: configuration is empty")
	}
	if cred.Name == "" {
		return fmt.Errorf("config: credential name is required")
	}
	for i := range c.Credentials {
		if c.Credentials[i].Name == cred.Name {
			c.Credentials[i] = cred
			return nil
		}
	}
	c.Credentials = append(c.Credentials, cred)
	return nil
}

// AdoptMountCredential moves a legacy mount's credential-scope parameters into
// a new credential and points the mount at it. "both"-scope parameters stay on
// the mount, where they take precedence.
func (c *Config) AdoptMountCredential(mountName, credentialName string) error {
	if c == nil {
		return fmt.Errorf("config: configuration is empty")
	}
	if mountName == "" {
		return fmt.Errorf("config: mount name is required")
	}
	if credentialName == "" {
		return fmt.Errorf("config: credential name is required")
	}
	if _, exists := c.CredentialFor(credentialName); exists {
		return fmt.Errorf("config: credential %q already exists", credentialName)
	}
	mount, ok := c.MountByName(mountName)
	if !ok {
		return fmt.Errorf("config: mount %q not found", mountName)
	}
	if mount.Credential != "" {
		return fmt.Errorf("config: mount %q already references credential %q", mountName, mount.Credential)
	}
	scopes := map[string]string{}
	for _, param := range drive.ParamSpecFor(mount.Type).Params {
		scopes[param.Name] = param.Scope
	}
	moved := ParamMap{}
	remaining := ParamMap{}
	for key, value := range mount.Params {
		if scopes[key] == drive.ParamScopeCredential {
			moved[key] = value
			continue
		}
		remaining[key] = value
	}
	if len(moved) == 0 {
		return fmt.Errorf("config: mount %q has no credential-scope parameters to adopt", mountName)
	}
	c.Credentials = append(c.Credentials, CredentialConfig{
		Name:   credentialName,
		Type:   mount.Type,
		Params: moved,
	})
	mount.Credential = credentialName
	mount.Params = remaining
	return c.UpsertMount(mount)
}

// RemoveCredential removes a credential that no mount references.
func (c *Config) RemoveCredential(name string) error {
	if c == nil {
		return fmt.Errorf("config: configuration is empty")
	}
	if name == "" {
		return fmt.Errorf("config: credential name is required")
	}
	if used := c.CredentialUsedBy(name); len(used) > 0 {
		return fmt.Errorf("config: credential %q is still referenced by mounts %v", name, used)
	}
	for i := range c.Credentials {
		if c.Credentials[i].Name == name {
			c.Credentials = append(c.Credentials[:i], c.Credentials[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("config: credential %q not found", name)
}
