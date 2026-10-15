// sync-contract-config updates the contract-test secret without writing credentials to disk.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/BurntSushi/toml"
)

func selectConfig(data, mountsJSON []byte) ([]byte, int, error) {
	var names []string
	if err := json.Unmarshal(mountsJSON, &names); err != nil || len(names) == 0 {
		return nil, 0, errors.New("CONTRACT_MOUNTS_JSON must be a non-empty JSON array of mount names")
	}
	wanted := make(map[string]bool)
	for _, name := range names {
		if strings.TrimSpace(name) == "" || wanted[name] {
			return nil, 0, errors.New("CONTRACT_MOUNTS_JSON contains an empty or duplicate name")
		}
		wanted[name] = true
	}
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		// Parser errors can quote credential-bearing input lines.
		return nil, 0, errors.New("invalid local TOML configuration (details suppressed to protect credentials)")
	}
	mounts, ok := cfg["mounts"].([]map[string]any)
	if !ok {
		return nil, 0, errors.New("configuration must contain [[mounts]] tables")
	}
	byName := make(map[string]map[string]any)
	for _, mount := range mounts {
		name, _ := mount["name"].(string)
		if name == "" || byName[name] != nil {
			return nil, 0, errors.New("configuration contains an empty or duplicate mount name")
		}
		byName[name] = mount
	}
	selected := make([]map[string]any, 0, len(names))
	for _, name := range names {
		mount := byName[name]
		if mount == nil {
			return nil, 0, fmt.Errorf("mount %q not found in local configuration", name)
		}
		if mount["test_enabled"] != true {
			return nil, 0, fmt.Errorf("mount %q requires test_enabled = true", name)
		}
		selected = append(selected, mount)
	}
	cfg["mounts"] = selected
	if upload, ok := cfg["upload"].(map[string]any); ok {
		if target, _ := upload["default_mount"].(string); target != "" && !wanted[target] {
			delete(upload, "default_mount")
			delete(upload, "default_path")
		}
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return nil, 0, errors.New("cannot encode selected configuration")
	}
	return buf.Bytes(), len(selected), nil
}

// Keep all command output private: gh errors must not echo secret input.
func gh(input io.Reader, args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	cmd.Stdin = input
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("gh command failed; check authentication and repository permissions")
	}
	return out, nil
}

func run() error {
	configPath := flag.String("config", "./qrypt.toml", "local configuration file")
	repo := flag.String("repo", "", "target OWNER/REPO (default: current repository)")
	dryRun := flag.Bool("dry-run", false, "validate selection without updating the secret")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *repo == "" {
		out, err := gh(nil, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
		if err != nil {
			return err
		}
		*repo = strings.TrimSpace(string(out))
	}
	mounts, err := gh(nil, "variable", "get", "CONTRACT_MOUNTS_JSON", "--repo", *repo)
	if err != nil {
		return fmt.Errorf("read CONTRACT_MOUNTS_JSON: %w", err)
	}
	data, err := os.ReadFile(*configPath)
	if err != nil {
		return errors.New("cannot read local configuration file")
	}
	selected, count, err := selectConfig(data, mounts)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(selected)
	if *dryRun {
		fmt.Printf("Validated %d mounts for %s; secret unchanged.\n", count, *repo)
		return nil
	}
	if _, err := gh(strings.NewReader(encoded), "secret", "set", "CONTRACT_CONFIG_TOML_B64", "--repo", *repo); err != nil {
		return fmt.Errorf("update CONTRACT_CONFIG_TOML_B64: %w", err)
	}
	fmt.Printf("Updated CONTRACT_CONFIG_TOML_B64 for %s (%d mounts).\n", *repo, count)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
