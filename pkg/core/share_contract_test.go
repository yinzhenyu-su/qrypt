package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// TestShareContractSupportsTwoProviderShapes pins the provider-variation
// contract with two non-Quark fixtures:
//
//   - alpha requires a passcode, finishes the save synchronously as a batch,
//     reports partial success per entry, saves into an opaque provider-supplied
//     destination id, and cannot list its own links.
//   - beta has no passcodes at all, has no share-link management surface, and
//     finishes a save asynchronously behind an opaque monitor handle.
//
// The Core must drive both through the same task creation, verification, and
// per-entry folding paths; no branch may depend on either provider.
func TestShareContractSupportsTwoProviderShapes(t *testing.T) {
	t.Parallel()

	alpha := newFakeReceiveProvider()
	alpha.linkPrefix = "https://alpha.test/s/"
	alpha.policy = drive.SharePolicy{
		Passcode:  drive.PolicyRequired,
		Expiries:  []time.Duration{drive.ShareExpiryWeek},
		Selection: true,
	}
	alpha.caps = []drive.Capability{
		drive.CapabilityWriter,
		drive.CapabilityShareReceive,
		drive.CapabilityShareSave,
		drive.CapabilityShareFetch,
	}
	alpha.entries = []drive.ShareEntry{
		{Entry: drive.Entry{ID: "a1", Name: "a.bin", Size: 10}, Ticket: "t1"},
		{Entry: drive.Entry{ID: "a2", Name: "b.bin", Size: 20}, Ticket: "t2"},
	}
	alpha.outcome = alpha.partialOutcome
	if err := alpha.Seed(map[string]string{"inbox/": ""}); err != nil {
		t.Fatal(err)
	}

	beta := newFakeReceiveProvider()
	beta.linkPrefix = "https://beta.test/s/"
	beta.policy = drive.SharePolicy{}
	beta.caps = []drive.Capability{
		drive.CapabilityWriter,
		drive.CapabilityShareReceive,
		drive.CapabilityShareSave,
	}
	beta.entries = []drive.ShareEntry{
		{Entry: drive.Entry{ID: "b1", Name: "c.bin", Size: 30}},
	}
	beta.handle = "monitor:beta"
	if err := beta.Seed(map[string]string{"a.bin": "content"}); err != nil {
		t.Fatal(err)
	}

	c := newReceiveCore(t, map[string]*fakeReceiveProvider{"alpha": alpha, "beta": beta}, nil)

	// alpha: synchronous batch, partial per-entry result.
	createdAlpha, err := c.CreateTask(context.Background(), task.Request{
		Type: task.TypeShareSave,
		Share: &task.ShareOperationRequest{
			Link: "https://alpha.test/s/one",
			To:   "/alpha/inbox",
		},
	})
	if err != nil {
		t.Fatalf("alpha create: %v", err)
	}
	alphaDone := waitCoreTask(t, c, createdAlpha.ID)
	if alphaDone.State != task.StatePartialFailed {
		t.Fatalf("alpha state = %s, detail = %+v", alphaDone.State, alphaDone.Detail)
	}
	if len(alpha.saveCalls) != 1 {
		t.Fatalf("alpha save calls = %d", len(alpha.saveCalls))
	}
	if dest := alpha.saveCalls[0].Dest.Entry.ID; dest != "0/inbox" {
		t.Fatalf("alpha destination id = %q, want the resolved opaque id", dest)
	}
	if alpha.saveCalls[0].Session.SessionToken == "" {
		t.Fatal("alpha save did not carry the provider session")
	}

	// beta: asynchronous monitor handle, no share-link management.
	createdBeta, err := c.CreateTask(context.Background(), task.Request{
		Type: task.TypeShareSave,
		Share: &task.ShareOperationRequest{
			Link:  "https://beta.test/s/two",
			Mount: "beta",
		},
	})
	if err != nil {
		t.Fatalf("beta create: %v", err)
	}
	betaDone := waitCoreTask(t, c, createdBeta.ID)
	if betaDone.State != task.StateSucceeded {
		t.Fatalf("beta state = %s, detail = %+v", betaDone.State, betaDone.Detail)
	}
	if beta.getCalls == 0 {
		t.Fatal("beta save did not poll its monitor handle")
	}

	// beta has no link-management capability, so share create/list fail the
	// same way any unimplemented provider does, before a provider call.
	if _, err := c.CreateShareLink(context.Background(), ShareLinkCreateRequest{Paths: []string{"/beta/a.bin"}}); !errors.Is(err, ErrShareUnsupported) {
		t.Fatalf("beta create link error = %v, want share_unsupported", err)
	}
	if _, err := c.ListShareLinks(context.Background(), ShareLinkListRequest{Mount: "beta"}); !errors.Is(err, ErrShareUnsupported) {
		t.Fatalf("beta list links error = %v, want share_unsupported", err)
	}
}
