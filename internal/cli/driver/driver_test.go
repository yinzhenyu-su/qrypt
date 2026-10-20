package driver

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all"
)

func TestDriverListJSON(t *testing.T) {
	cmd := NewListCmd(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal(out.Bytes(), &names); err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("expected registered drivers")
	}
}

func TestDriverSchemaJSONIncludesRequirements(t *testing.T) {
	cmd := NewSchemaCmd(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, []string{"sftp"}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Name       string `json:"name"`
		Parameters []struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			Required    bool   `json:"required"`
			Secret      bool   `json:"secret"`
			Description string `json:"description"`
			Default     string `json:"default"`
			Example     string `json:"example"`
		} `json:"parameters"`
		Required []string   `json:"required"`
		AnyOf    [][]string `json:"any_of"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "sftp" {
		t.Fatalf("name = %q, want sftp", got.Name)
	}
	if len(got.Required) != 2 || got.Required[0] != "address" || got.Required[1] != "username" {
		t.Fatalf("required = %v, want [address username]", got.Required)
	}
	if len(got.AnyOf) != 2 || len(got.AnyOf[0]) != 1 || got.AnyOf[0][0] != "password" {
		t.Fatalf("any_of = %v, want [[password] [private_key]]", got.AnyOf)
	}
	for _, param := range got.Parameters {
		if param.Name == "password" && !param.Secret {
			t.Fatal("password must be marked secret")
		}
	}
}

func TestDriverSchemaTextUsesLocalizedDescriptions(t *testing.T) {
	cmd := NewSchemaCmd(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	i18n.Attach(cmd, i18n.LangZHCN)
	if err := cmd.RunE(cmd, []string{"localfs"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "本地文件系统根目录路径") {
		t.Fatalf("localized driver schema = %q", out.String())
	}
}
