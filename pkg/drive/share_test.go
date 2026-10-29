package drive

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// sharePolicyDriver is a fake driver that declares a share surface.
type sharePolicyDriver struct {
	*FakeDriver
	caps   []Capability
	policy SharePolicy
}

func (d *sharePolicyDriver) Capabilities() []Capability { return d.caps }
func (d *sharePolicyDriver) SharePolicy() SharePolicy   { return d.policy }

func TestShareTypesJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	entry := Entry{ID: "f1", ParentID: "0", Name: "a.bin", Size: 42, IsDir: false}
	saved := Entry{ID: "n1", ParentID: "dest", Name: "a.bin", Size: 42}

	values := map[string]any{
		"link": ShareLink{URL: "https://pan.quark.cn/s/abc", LinkID: "abc", Passcode: "9k2f"},
		"share": Share{
			NativeID: "native", LinkID: "abc", URL: "https://pan.quark.cn/s/abc",
			Title: "t", Owner: ShareOwner{Name: "n", MemberType: "SUPER_VIP"},
			Public: false, Passcode: "9k2f", ExpiresAt: now,
			VisitLimit: 10, VisitCount: 2, EntryCount: 3, Size: 1234,
			CreatedAt: now, UpdatedAt: now, Risk: "ok",
		},
		"session": ShareSession{
			Link:         ShareLink{URL: "u", LinkID: "abc"},
			SessionToken: "secret", ExpiresAt: now,
			Share: Share{NativeID: "n", LinkID: "abc", URL: "u", Title: "t"},
		},
		"entry": ShareEntry{Entry: entry, Ticket: "ticket", RootID: "root"},
		"dest":  ShareDestination{Entry: saved, SpaceID: "ws1", Rename: RenameAuto},
		"outcome": ShareSaveOutcome{
			Handle: "handle", State: SaveStatePartial, Progress: 0.5,
			Items: []ShareSaveItem{
				{SourceID: "f1", Saved: &saved},
				{SourceID: "f2", SavedID: "n2", SavedName: "b.bin", Verified: true},
				{SourceID: "f3", Error: NewShareError(ShareErrQuota, "32003", "no space")},
			},
		},
		"policy": SharePolicy{
			Passcode: PolicyRequired,
			Expiries: []time.Duration{ShareExpiryDay, ShareExpiryPermanent},
			Title:    true, Public: true, Download: true, Edit: true, MaxEntries: 3,
			Update: true, List: true,
			Rename: ShareRenameSupport{Auto: true},
		},
		"target": DownloadTarget{URL: "https://dl", ExpiresAt: now, Size: 7, Headers: map[string]string{"x-share-token": "t"}},
	}

	for name, value := range values {
		t.Run(name, func(t *testing.T) {
			first, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			ptr := reflect.New(reflect.TypeOf(value)).Interface()
			if err := json.Unmarshal(first, ptr); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			second, err := json.Marshal(ptr)
			if err != nil {
				t.Fatalf("re-marshal: %v", err)
			}
			if string(first) != string(second) {
				t.Fatalf("round trip changed the payload:\n first: %s\nsecond: %s", first, second)
			}
		})
	}
}

func isZeroSharePolicy(p SharePolicy) bool {
	return p.Passcode == "" && len(p.Expiries) == 0 && !p.Title && !p.Public && !p.Download &&
		!p.Edit && !p.VisitLimit && !p.Update && !p.List && p.MaxEntries == 0 &&
		p.Rename == (ShareRenameSupport{})
}

func TestSharePolicyForRequiresCapability(t *testing.T) {
	policy := SharePolicy{Passcode: PolicyOptional, Expiries: NeutralShareExpiries()}

	withoutCap := &sharePolicyDriver{FakeDriver: NewFakeDriver(), policy: policy}
	if got := SharePolicyFor(withoutCap); !isZeroSharePolicy(got) {
		t.Fatalf("driver without CapabilityShare reported policy %+v", got)
	}

	withCap := &sharePolicyDriver{FakeDriver: NewFakeDriver(), caps: []Capability{CapabilityShare}, policy: policy}
	if got := SharePolicyFor(withCap); got.Passcode != PolicyOptional {
		t.Fatalf("declared policy was not reported: %+v", got)
	}

	// A receive-only provider still declares passcode and selection policy even
	// though it cannot manage links.
	receiveOnly := &sharePolicyDriver{FakeDriver: NewFakeDriver(), caps: []Capability{CapabilityShareReceive}, policy: policy}
	if got := SharePolicyFor(receiveOnly); got.Passcode != PolicyOptional {
		t.Fatalf("receive-only policy was not reported: %+v", got)
	}

	// A driver that declares the capability but no reporter has no options.
	silent := &sharePolicyDriver{FakeDriver: NewFakeDriver(), caps: []Capability{CapabilityShare}}
	if got := SharePolicyFor(silent); !isZeroSharePolicy(got) {
		t.Fatalf("driver without a reporter reported policy %+v", got)
	}

	if got := SharePolicyFor(nil); !isZeroSharePolicy(got) {
		t.Fatalf("nil driver reported policy %+v", got)
	}
}

func TestShareCapabilitiesAreReported(t *testing.T) {
	d := &sharePolicyDriver{
		FakeDriver: NewFakeDriver(),
		caps: []Capability{
			CapabilityShare, CapabilityShareReceive, CapabilityShareSave, CapabilityShareFetch,
			CapabilityShare, // duplicate must be normalized away
		},
	}
	for _, capability := range []Capability{
		CapabilityShare, CapabilityShareReceive, CapabilityShareSave, CapabilityShareFetch,
	} {
		if !HasCapability(d, capability) {
			t.Fatalf("missing %s", capability)
		}
	}
	if got := Capabilities(d); len(got) != 4 {
		t.Fatalf("capabilities = %v, want four normalized entries", got)
	}
	if HasCapability(NewFakeDriver(), CapabilityShare) {
		t.Fatal("plain fake driver reported a share capability")
	}
}

func TestSharePolicyOptionChecks(t *testing.T) {
	policy := SharePolicy{
		Passcode: PolicyRequired,
		Expiries: []time.Duration{ShareExpiryDay, ShareExpiryPermanent},
		Update:   true,
		List:     true,
		Rename:   ShareRenameSupport{Auto: true},
	}
	if !policy.SupportsExpiry(ShareExpiryDay) || !policy.SupportsExpiry(ShareExpiryPermanent) {
		t.Fatal("declared expiries were rejected")
	}
	if policy.SupportsExpiry(ShareExpiryWeek) {
		t.Fatal("undeclared expiry was accepted")
	}
	if !policy.SupportsRename(RenameFail) {
		t.Fatal("fail-on-collision must always be available")
	}
	if !policy.SupportsRename(RenameAuto) {
		t.Fatal("declared auto rename was rejected")
	}
	if policy.SupportsRename(RenameOverwrite) {
		t.Fatal("undeclared overwrite was accepted")
	}
	if policy.SupportsRename(RenamePolicy("bogus")) {
		t.Fatal("unknown rename policy was accepted")
	}
}

func TestSharePolicyValidatesCreateOptions(t *testing.T) {
	mandatory := SharePolicy{
		Passcode: PolicyRequired,
		Expiries: []time.Duration{ShareExpiryDay},
	}
	if err := mandatory.ValidateCreateRequest(ShareCreateRequest{Expiry: ShareExpiryDay}); err == nil {
		t.Fatal("missing passcode was accepted by a mandatory-passcode policy")
	}
	if err := mandatory.ValidateCreateRequest(ShareCreateRequest{Expiry: ShareExpiryDay, Passcode: "abcd"}); err != nil {
		t.Fatalf("valid passcode rejected: %v", err)
	}

	passcodeLess := SharePolicy{Passcode: PolicyUnsupported, Expiries: []time.Duration{ShareExpiryPermanent}}
	if err := passcodeLess.ValidateCreateRequest(ShareCreateRequest{Passcode: "abcd"}); err == nil {
		t.Fatal("passcode was accepted by a provider that has none")
	}
	if err := passcodeLess.ValidateCreateRequest(ShareCreateRequest{}); err != nil {
		t.Fatalf("permanent passcode-less create rejected: %v", err)
	}

	if err := passcodeLess.ValidateCreateRequest(ShareCreateRequest{Expiry: ShareExpiryWeek}); err == nil {
		t.Fatal("unsupported expiry was accepted")
	}

	noVisits := SharePolicy{Passcode: PolicyOptional, Expiries: []time.Duration{ShareExpiryPermanent}, VisitLimit: false}
	if err := noVisits.ValidateCreateRequest(ShareCreateRequest{VisitLimit: 5}); err == nil {
		t.Fatal("visit limit was accepted by a provider without it")
	}

	noList := SharePolicy{Passcode: PolicyOptional, Expiries: []time.Duration{ShareExpiryPermanent}, List: false}
	if err := noList.ValidateListRequest(ShareListRequest{}); err == nil {
		t.Fatal("listing was accepted by a provider without a my-links list")
	}
	if err := (SharePolicy{List: true}).ValidateListRequest(ShareListRequest{}); err != nil {
		t.Fatalf("declared listing rejected: %v", err)
	}

	expiries := []time.Duration{ShareExpiryPermanent}
	if err := (SharePolicy{Expiries: expiries}).ValidateCreateRequest(ShareCreateRequest{Title: "release"}); err == nil {
		t.Fatal("title was accepted by a provider without titles")
	}
	yes := true
	for name, req := range map[string]ShareCreateRequest{
		"public":   {Public: &yes},
		"download": {Download: &yes},
		"edit":     {Edit: &yes},
	} {
		if err := (SharePolicy{Expiries: expiries}).ValidateCreateRequest(req); err == nil {
			t.Fatalf("%s option was accepted by a provider without it", name)
		}
	}
	limited := SharePolicy{Expiries: expiries, MaxEntries: 1, Title: true, Public: true, Download: true, Edit: true}
	if err := limited.ValidateCreateRequest(ShareCreateRequest{Entries: []Entry{{ID: "a"}, {ID: "b"}}}); err == nil {
		t.Fatal("multi-entry request exceeded the declared maximum")
	}
	if err := limited.ValidateCreateRequest(ShareCreateRequest{Entries: []Entry{{ID: "a"}}, Title: "t", Public: &yes, Download: &yes, Edit: &yes}); err != nil {
		t.Fatalf("declared options rejected: %v", err)
	}
}

func TestSharePolicyValidatesUpdatesAndSaves(t *testing.T) {
	noUpdate := SharePolicy{Passcode: PolicyOptional, Expiries: []time.Duration{ShareExpiryPermanent}}
	if err := noUpdate.ValidateUpdateRequest(ShareUpdateRequest{ID: "x"}); err == nil {
		t.Fatal("update was accepted by a provider without update support")
	}

	mandatory := SharePolicy{Passcode: PolicyRequired, Expiries: []time.Duration{ShareExpiryDay}, Update: true}
	empty := ""
	if err := mandatory.ValidateUpdateRequest(ShareUpdateRequest{ID: "x", Passcode: &empty}); err == nil {
		t.Fatal("empty passcode accepted by a mandatory-passcode policy")
	}
	if err := mandatory.ValidateUpdateRequest(ShareUpdateRequest{ID: "x"}); err != nil {
		t.Fatalf("policy-only update rejected: %v", err)
	}
	public := true
	if err := mandatory.ValidateUpdateRequest(ShareUpdateRequest{ID: "x", Public: &public}); err == nil {
		t.Fatal("public update accepted by a mandatory-passcode policy")
	}
	title := "release"
	if err := mandatory.ValidateUpdateRequest(ShareUpdateRequest{ID: "x", Title: &title}); err == nil {
		t.Fatal("title update accepted by a provider without titles")
	}
	yes := true
	for name, req := range map[string]ShareUpdateRequest{
		"public":   {ID: "x", Public: &yes},
		"download": {ID: "x", Download: &yes},
		"edit":     {ID: "x", Edit: &yes},
	} {
		if err := mandatory.ValidateUpdateRequest(req); err == nil {
			t.Fatalf("%s update accepted by a provider without it", name)
		}
	}

	noOverwrite := SharePolicy{Expiries: []time.Duration{ShareExpiryPermanent}, Rename: ShareRenameSupport{Auto: true}}
	if err := noOverwrite.ValidateSaveRequest(ShareSaveRequest{Dest: ShareDestination{Rename: RenameOverwrite}}); err == nil {
		t.Fatal("overwrite save accepted without overwrite support")
	}
	if err := noOverwrite.ValidateSaveRequest(ShareSaveRequest{Dest: ShareDestination{Rename: RenameAuto}}); err != nil {
		t.Fatalf("declared auto rename rejected: %v", err)
	}
}

func TestShareErrorClassification(t *testing.T) {
	cases := []struct {
		code      ShareErrorCode
		class     ShareErrorClass
		retryable bool
	}{
		{ShareErrLinkInvalid, ShareErrorUsage, false},
		{ShareErrLinkProviderMismatch, ShareErrorUsage, false},
		{ShareErrCrossProviderUnsupported, ShareErrorUsage, false},
		{ShareErrUnsupported, ShareErrorUsage, false},
		{ShareErrNotFound, ShareErrorNotFound, false},
		{ShareErrExpired, ShareErrorNotFound, false},
		{ShareErrPasscodeRequired, ShareErrorRuntime, false},
		{ShareErrPasscodeWrong, ShareErrorRuntime, false},
		{ShareErrOwnSave, ShareErrorRuntime, false},
		{ShareErrTicketInvalid, ShareErrorRuntime, false},
		{ShareErrVisitLimit, ShareErrorRuntime, false},
		{ShareErrQuota, ShareErrorRuntime, false},
		{ShareErrVerificationRequired, ShareErrorRuntime, false},
		{ShareErrRateLimited, ShareErrorRuntime, true},
		{ShareErrTimeout, ShareErrorRuntime, true},
		{ShareErrDestinationUnverified, ShareErrorRuntime, false},
		{ShareErrProvider, ShareErrorRuntime, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			err := NewShareError(tc.code, "123", "boom")
			if err.Class != tc.class {
				t.Fatalf("class = %s, want %s", err.Class, tc.class)
			}
			if err.Retryable != tc.retryable {
				t.Fatalf("retryable = %v, want %v", err.Retryable, tc.retryable)
			}
			wrapped := fmt.Errorf("wrapped: %w", err)
			found, ok := AsShareError(wrapped)
			if !ok || found.Code != tc.code {
				t.Fatalf("AsShareError = %+v, %v", found, ok)
			}
			if !IsShareErrorCode(wrapped, tc.code) {
				t.Fatalf("IsShareErrorCode(%s) = false", tc.code)
			}
			if IsShareErrorCode(wrapped, ShareErrNotFound) && tc.code != ShareErrNotFound {
				t.Fatal("IsShareErrorCode matched the wrong code")
			}
		})
	}

	if _, ok := AsShareError(errors.New("plain")); ok {
		t.Fatal("AsShareError matched a plain error")
	}
}

func TestShareSaveOutcomeComplete(t *testing.T) {
	for state, want := range map[SaveState]bool{
		SaveStateQueued:    false,
		SaveStateRunning:   false,
		SaveStateSucceeded: true,
		SaveStatePartial:   true,
		SaveStateFailed:    true,
	} {
		if got := (ShareSaveOutcome{State: state}).Complete(); got != want {
			t.Fatalf("state %s Complete() = %v, want %v", state, got, want)
		}
	}
}
