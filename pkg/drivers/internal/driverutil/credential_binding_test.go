package driverutil

import (
	"encoding/json"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/credentialtest"
)

type bindingPayload struct {
	Value string `json:"value"`
}

func TestCredentialBindingReplaceHealsCorruptPayload(t *testing.T) {
	session := credentialtest.New("main")
	if _, err := session.Update(0, []byte("{not-json"), ""); err != nil {
		t.Fatal(err)
	}
	applied := ""
	binding := NewCredentialBinding("test",
		func(data []byte) (bindingPayload, error) {
			var payload bindingPayload
			err := json.Unmarshal(data, &payload)
			return payload, err
		},
		func(payload bindingPayload, _ drive.CredentialSnapshot, _ string) bool {
			if payload.Value == "" {
				return false
			}
			applied = payload.Value
			return true
		})
	binding.Install(session)
	if binding.Load() {
		t.Fatal("corrupt payload should not load")
	}
	if err := binding.Replace([]byte(`{"value":"recovered"}`), "account-a"); err != nil {
		t.Fatal(err)
	}
	if applied != "recovered" {
		t.Fatalf("applied = %q, want the healed payload", applied)
	}
	if got := session.Snapshot().Account; got != "account-a" {
		t.Fatalf("session account = %q, want account-a", got)
	}
	if binding.Version() != session.Snapshot().Version {
		t.Fatalf("binding version = %d, session version = %d", binding.Version(), session.Snapshot().Version)
	}
}
