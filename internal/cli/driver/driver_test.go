package driver

import (
	"bytes"
	"encoding/json"
	"testing"

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
