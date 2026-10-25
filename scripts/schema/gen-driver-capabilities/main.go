// Command gen-driver-capabilities prints the built-in drivers' runtime
// capabilities as JSON for scripts/schema/gen-driver-docs.py.
//
// The constructors only materialize a driver; they do not talk to a backend.
// Parameters are dummies that satisfy each factory's validation.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all"
)

type driverCapabilities struct {
	Capabilities []string `json:"capabilities"`
	RemoteHash   bool     `json:"remote_hash"`
	Login        bool     `json:"login"`
}

type driverSpec struct {
	Name   string
	Params drive.Params
}

var drivers = []driverSpec{
	{Name: "localfs", Params: drive.Params{"root_path": "/tmp"}},
	{Name: "aliyundrive", Params: drive.Params{
		"refresh_token": "token",
		"drive_id":      "drive",
	}},
	{Name: "baidu_netdisk", Params: drive.Params{"refresh_token": "token"}},
	{Name: "onedrive", Params: drive.Params{"refresh_token": "token"}},
	{Name: "onedrive_app", Params: drive.Params{
		"client_id":     "client",
		"client_secret": "secret",
		"tenant_id":     "tenant",
		"email":         "user@example.com",
	}},
	{Name: "quark", Params: drive.Params{"cookie": "k=v"}},
	{Name: "yun139", Params: drive.Params{"authorization": "token"}},
	{Name: "115", Params: drive.Params{"cookie": "k=v"}},
	{Name: "115_open", Params: drive.Params{"refresh_token": "token"}},
	{Name: "123", Params: drive.Params{
		"username": "user",
		"password": "password",
	}},
	{Name: "189", Params: drive.Params{"cookie": "k=v"}},
	{Name: "webdav", Params: drive.Params{
		"url":      "https://example.invalid/",
		"username": "user",
		"password": "password",
	}},
	{Name: "sftp", Params: drive.Params{
		"address":  "example.invalid:22",
		"username": "user",
		"password": "password",
	}},
	{Name: "s3", Params: drive.Params{
		"bucket":            "bucket",
		"endpoint":          "https://example.invalid",
		"access_key_id":     "access",
		"secret_access_key": "secret",
	}},
}

func main() {
	out := make(map[string]driverCapabilities, len(drivers))
	for _, spec := range drivers {
		drv, err := drive.New(spec.Name, spec.Params)
		if err != nil {
			fmt.Fprintf(os.Stderr, "construct %s: %v\n", spec.Name, err)
			os.Exit(1)
		}

		capabilities := drive.Capabilities(drv)
		names := make([]string, len(capabilities))
		for i, capability := range capabilities {
			names[i] = string(capability)
		}
		_, remoteHash := drv.(drive.RemoteHasher)
		_, login := drive.LoginProviderFor(spec.Name)
		out[spec.Name] = driverCapabilities{
			Capabilities: names,
			RemoteHash:   remoteHash,
			Login:        login,
		}
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "encode capabilities: %v\n", err)
		os.Exit(1)
	}
}
