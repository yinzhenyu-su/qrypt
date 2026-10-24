package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// fakeShareProvider is an in-memory driver that implements the share-manager
// half of the provider contract on top of FakeDriver's file tree.
type fakeShareProvider struct {
	*drive.FakeDriver

	mu     sync.Mutex
	caps   []drive.Capability
	policy drive.SharePolicy

	shares []drive.Share
	next   int

	createCalls []drive.ShareCreateRequest
	listCalls   int
	deleted     []string
	updates     []drive.ShareUpdateRequest

	createErr error
	getErr    error
	// block, when non-nil, makes CreateShare wait for release or ctx.Done.
	block chan struct{}
}

func newFakeShareProvider() *fakeShareProvider {
	return &fakeShareProvider{
		FakeDriver: drive.NewFakeDriver(),
		caps:       []drive.Capability{drive.CapabilityShare},
		policy: drive.SharePolicy{
			Passcode: drive.PolicyOptional,
			Expiries: drive.NeutralShareExpiries(),
			Update:   true,
			List:     true,
			Rename:   drive.ShareRenameSupport{Auto: true},
		},
	}
}

func (p *fakeShareProvider) Capabilities() []drive.Capability { return p.caps }
func (p *fakeShareProvider) SharePolicy() drive.SharePolicy   { return p.policy }

func (p *fakeShareProvider) addShare(share drive.Share) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shares = append(p.shares, share)
}

func (p *fakeShareProvider) CreateShare(ctx context.Context, req drive.ShareCreateRequest) (drive.Share, error) {
	if p.createErr != nil {
		return drive.Share{}, p.createErr
	}
	if p.block != nil {
		select {
		case <-ctx.Done():
			return drive.Share{}, ctx.Err()
		case <-p.block:
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	share := drive.Share{
		NativeID:   fmt.Sprintf("native%08d", p.next),
		LinkID:     fmt.Sprintf("link%08d", p.next),
		URL:        fmt.Sprintf("https://example.test/s/link%08d", p.next),
		Title:      req.Title,
		Passcode:   req.Passcode,
		Public:     req.Passcode == "",
		EntryCount: len(req.Entries),
	}
	p.shares = append(p.shares, share)
	p.createCalls = append(p.createCalls, req)
	return share, nil
}

func (p *fakeShareProvider) ListShares(ctx context.Context, req drive.ShareListRequest) (drive.SharePage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listCalls++
	return drive.SharePage{Shares: append([]drive.Share(nil), p.shares...)}, nil
}

func (p *fakeShareProvider) GetShare(ctx context.Context, id string) (drive.Share, error) {
	if p.getErr != nil {
		return drive.Share{}, p.getErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, share := range p.shares {
		if share.NativeID == id || share.LinkID == id || share.URL == id {
			return share, nil
		}
	}
	return drive.Share{}, drive.NewShareError(drive.ShareErrNotFound, "", "no such link")
}

func (p *fakeShareProvider) UpdateShare(ctx context.Context, req drive.ShareUpdateRequest) (drive.Share, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updates = append(p.updates, req)
	for i := range p.shares {
		if p.shares[i].NativeID != req.ID {
			continue
		}
		if req.Title != nil {
			p.shares[i].Title = *req.Title
		}
		if req.Expiry != nil && *req.Expiry == drive.ShareExpiryPermanent {
			p.shares[i].ExpiresAt = time.Time{}
		}
		if req.Passcode != nil {
			p.shares[i].Passcode = *req.Passcode
		}
		if req.Public != nil {
			p.shares[i].Public = *req.Public
		}
		return p.shares[i], nil
	}
	return drive.Share{}, drive.NewShareError(drive.ShareErrNotFound, "", "no such link")
}

func (p *fakeShareProvider) DeleteShare(ctx context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deleted = append(p.deleted, id)
	for i := range p.shares {
		if p.shares[i].NativeID == id {
			p.shares = append(p.shares[:i], p.shares[i+1:]...)
			return nil
		}
	}
	return drive.NewShareError(drive.ShareErrNotFound, "", "no such link")
}

type shareMountSpec struct {
	name      string
	provider  *fakeShareProvider
	encrypted bool
}

func newShareCore(t *testing.T, providers map[string]*fakeShareProvider) *Core {
	t.Helper()
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]shareMountSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, shareMountSpec{name: name, provider: providers[name]})
	}
	return newShareCoreWithMounts(t, specs...)
}

func newShareCoreWithMounts(t *testing.T, specs ...shareMountSpec) *Core {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mounts := make([]vfs.Mount, 0, len(specs))
	for _, spec := range specs {
		fs, err := vfs.New(spec.provider, vfs.Options{
			Name:       spec.name,
			StorageDir: filepath.Join(t.TempDir(), spec.name+"-cache"),
			Encrypted:  spec.encrypted,
		})
		if err != nil {
			t.Fatalf("vfs.New: %v", err)
		}
		mounts = append(mounts, vfs.Mount{Name: spec.name, FS: fs})
	}
	ns, err := vfs.NewNamespace(mounts)
	if err != nil {
		t.Fatalf("vfs.NewNamespace: %v", err)
	}
	t.Cleanup(func() { stopTestVFS(t, ns) })
	ns.Start(ctx)
	return newTestCore(t, ns)
}

func TestCreateShareLinkResolvesEntriesOnOneMount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeShareProvider()
	if err := provider.Seed(map[string]string{"docs/": "", "docs/a.bin": "x"}); err != nil {
		t.Fatal(err)
	}
	c := newShareCore(t, map[string]*fakeShareProvider{"quark": provider})

	share, err := c.CreateShareLink(ctx, ShareLinkCreateRequest{
		Paths:  []string{"/quark/docs/a.bin", "/quark/docs"},
		Title:  "release",
		Expiry: drive.ShareExpiryWeek,
	})
	if err != nil {
		t.Fatalf("CreateShareLink: %v", err)
	}
	if share.LinkID == "" || share.URL == "" {
		t.Fatalf("provider share has no identity: %+v", share)
	}
	if len(provider.createCalls) != 1 {
		t.Fatalf("create calls = %d, want 1", len(provider.createCalls))
	}
	call := provider.createCalls[0]
	if len(call.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(call.Entries))
	}
	if call.Title != "release" || call.Expiry != drive.ShareExpiryWeek {
		t.Fatalf("create request = %+v", call)
	}
}

func TestCreateShareLinkRejectsCrossMountPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	first, second := newFakeShareProvider(), newFakeShareProvider()
	for _, provider := range []*fakeShareProvider{first, second} {
		if err := provider.Seed(map[string]string{"docs/": "", "docs/a.bin": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	c := newShareCore(t, map[string]*fakeShareProvider{"quark": first, "other": second})

	_, err := c.CreateShareLink(ctx, ShareLinkCreateRequest{
		Paths: []string{"/quark/docs/a.bin", "/other/docs/a.bin"},
	})
	if !errors.Is(err, ErrShareCrossMount) {
		t.Fatalf("error = %v, want ErrShareCrossMount", err)
	}
	if len(first.createCalls) != 0 || len(second.createCalls) != 0 {
		t.Fatal("a provider call happened for a rejected cross-mount request")
	}
}

func TestCreateShareLinkRejectsBadPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeShareProvider()
	if err := provider.Seed(map[string]string{"docs/": ""}); err != nil {
		t.Fatal(err)
	}
	c := newShareCore(t, map[string]*fakeShareProvider{"quark": provider})

	cases := []struct {
		name  string
		paths []string
		want  error
	}{
		{name: "no paths", paths: nil, want: ErrSharePathsRequired},
		{name: "missing path", paths: []string{"/quark/docs/missing.bin"}, want: ErrSharePathNotFound},
		{name: "mount root", paths: []string{"/quark"}, want: ErrShareRootPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.CreateShareLink(ctx, ShareLinkCreateRequest{Paths: tc.paths})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	if len(provider.createCalls) != 0 {
		t.Fatal("a provider call happened for a rejected path set")
	}
}

func TestCreateShareLinkValidatesOptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeShareProvider()
	if err := provider.Seed(map[string]string{"docs/": "", "docs/a.bin": "x"}); err != nil {
		t.Fatal(err)
	}
	c := newShareCore(t, map[string]*fakeShareProvider{"quark": provider})
	paths := []string{"/quark/docs/a.bin"}

	provider.policy = drive.SharePolicy{Passcode: drive.PolicyRequired, Expiries: drive.NeutralShareExpiries()}
	if _, err := c.CreateShareLink(ctx, ShareLinkCreateRequest{Paths: paths}); err == nil {
		t.Fatal("missing passcode accepted on a mandatory-passcode provider")
	}

	provider.policy = drive.SharePolicy{Passcode: drive.PolicyOptional, Expiries: drive.NeutralShareExpiries()}
	if _, err := c.CreateShareLink(ctx, ShareLinkCreateRequest{Paths: paths, VisitLimit: 5}); err == nil {
		t.Fatal("visit limit accepted on a provider without it")
	}

	provider.policy = drive.SharePolicy{Passcode: drive.PolicyOptional, Expiries: []time.Duration{drive.ShareExpiryDay}}
	if _, err := c.CreateShareLink(ctx, ShareLinkCreateRequest{Paths: paths, Expiry: drive.ShareExpiryWeek}); err == nil {
		t.Fatal("unsupported expiry accepted")
	}

	if len(provider.createCalls) != 0 {
		t.Fatalf("provider was called %d times for rejected options", len(provider.createCalls))
	}
}

func TestCreateShareLinkGeneratesPasscode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeShareProvider()
	if err := provider.Seed(map[string]string{"docs/": "", "docs/a.bin": "x"}); err != nil {
		t.Fatal(err)
	}
	c := newShareCore(t, map[string]*fakeShareProvider{"quark": provider})

	share, err := c.CreateShareLink(ctx, ShareLinkCreateRequest{
		Paths:            []string{"/quark/docs/a.bin"},
		GeneratePasscode: true,
	})
	if err != nil {
		t.Fatalf("CreateShareLink: %v", err)
	}
	if len(share.Passcode) != 4 {
		t.Fatalf("passcode = %q, want four characters", share.Passcode)
	}
	if provider.createCalls[0].Passcode != share.Passcode {
		t.Fatalf("provider got %q, returned %q", provider.createCalls[0].Passcode, share.Passcode)
	}
	if share.Public {
		t.Fatal("a passcode-protected link was reported as public")
	}
}

func TestCreateShareLinkUnsupportedProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeShareProvider()
	provider.caps = nil
	if err := provider.Seed(map[string]string{"docs/": "", "docs/a.bin": "x"}); err != nil {
		t.Fatal(err)
	}
	c := newShareCore(t, map[string]*fakeShareProvider{"quark": provider})

	if _, err := c.CreateShareLink(ctx, ShareLinkCreateRequest{Paths: []string{"/quark/docs/a.bin"}}); !errors.Is(err, ErrShareUnsupported) {
		t.Fatalf("error = %v, want ErrShareUnsupported", err)
	}
	if len(provider.createCalls) != 0 {
		t.Fatal("unsupported provider was called")
	}
}

// TestCreateShareLinkAllowsEncryptedMount pins the rule that sharing is
// allowed from an encrypted mount: only the receive destination is restricted.
func TestCreateShareLinkAllowsEncryptedMount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeShareProvider()
	if err := provider.Seed(map[string]string{"docs/": "", "docs/a.bin": "x"}); err != nil {
		t.Fatal(err)
	}
	c := newShareCoreWithMounts(t, shareMountSpec{name: "vault", provider: provider, encrypted: true})

	share, err := c.CreateShareLink(ctx, ShareLinkCreateRequest{Paths: []string{"/vault/docs/a.bin"}})
	if err != nil {
		t.Fatalf("CreateShareLink on an encrypted mount: %v", err)
	}
	if share.LinkID == "" {
		t.Fatal("no link was created")
	}
	if len(provider.createCalls) != 1 {
		t.Fatalf("create calls = %d, want 1", len(provider.createCalls))
	}
}

func TestCreateShareLinkTimesOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeShareProvider()
	if err := provider.Seed(map[string]string{"docs/": "", "docs/a.bin": "x"}); err != nil {
		t.Fatal(err)
	}
	provider.block = make(chan struct{})
	c := newShareCore(t, map[string]*fakeShareProvider{"quark": provider})
	c.shareLinkOpTimeout = 20 * time.Millisecond

	_, err := c.CreateShareLink(ctx, ShareLinkCreateRequest{Paths: []string{"/quark/docs/a.bin"}})
	if !drive.IsShareErrorCode(err, drive.ShareErrTimeout) {
		t.Fatalf("error = %v, want share_timeout", err)
	}
}

func TestListShareLinksHonorsListPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeShareProvider()
	provider.addShare(drive.Share{NativeID: "native1", LinkID: "link00000001", URL: "https://example.test/s/1", Title: "one"})
	c := newShareCore(t, map[string]*fakeShareProvider{"quark": provider})

	page, err := c.ListShareLinks(ctx, ShareLinkListRequest{Mount: "quark"})
	if err != nil {
		t.Fatalf("ListShareLinks: %v", err)
	}
	if len(page.Shares) != 1 || page.Shares[0].Title != "one" {
		t.Fatalf("page = %+v", page)
	}

	provider.policy.List = false
	if _, err := c.ListShareLinks(ctx, ShareLinkListRequest{Mount: "quark"}); !errors.Is(err, ErrShareUnsupported) {
		t.Fatalf("error = %v, want ErrShareUnsupported", err)
	}
}

func TestShareLinkMountSelection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	first, second := newFakeShareProvider(), newFakeShareProvider()
	c := newShareCore(t, map[string]*fakeShareProvider{"alpha": first, "beta": second})

	if _, err := c.ListShareLinks(ctx, ShareLinkListRequest{}); !errors.Is(err, ErrShareMountRequired) {
		t.Fatalf("error = %v, want ErrShareMountRequired", err)
	}
	if _, err := c.ListShareLinks(ctx, ShareLinkListRequest{Mount: "missing"}); !errors.Is(err, ErrShareMountNotFound) {
		t.Fatalf("error = %v, want ErrShareMountNotFound", err)
	}
}

func TestRemoveShareLinkResolvesLinkID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeShareProvider()
	provider.addShare(drive.Share{NativeID: "native1", LinkID: "link00000001", URL: "https://example.test/s/1"})
	c := newShareCore(t, map[string]*fakeShareProvider{"quark": provider})

	if err := c.RemoveShareLink(ctx, "quark", "link00000001"); err != nil {
		t.Fatalf("RemoveShareLink: %v", err)
	}
	if len(provider.deleted) != 1 || provider.deleted[0] != "native1" {
		t.Fatalf("deleted = %v, want the native id", provider.deleted)
	}

	if err := c.RemoveShareLink(ctx, "quark", "link99999999"); !drive.IsShareErrorCode(err, drive.ShareErrNotFound) {
		t.Fatalf("error = %v, want share_not_found", err)
	}
}

func TestGetAndUpdateShareLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeShareProvider()
	provider.addShare(drive.Share{NativeID: "native1", LinkID: "link00000001", URL: "https://example.test/s/1", Title: "old"})
	c := newShareCore(t, map[string]*fakeShareProvider{"quark": provider})

	byURL, err := c.GetShareLink(ctx, "quark", "https://example.test/s/1")
	if err != nil || byURL.NativeID != "native1" {
		t.Fatalf("GetShareLink = %+v, %v", byURL, err)
	}

	title := "new"
	updated, err := c.UpdateShareLink(ctx, ShareLinkUpdateRequest{Mount: "quark", ID: "link00000001", Title: &title})
	if err != nil {
		t.Fatalf("UpdateShareLink: %v", err)
	}
	if updated.Title != "new" || updated.NativeID != "native1" {
		t.Fatalf("updated = %+v", updated)
	}
	if len(provider.updates) != 1 || provider.updates[0].ID != "native1" {
		t.Fatalf("updates = %+v, want the native id", provider.updates)
	}

	if _, err := c.GetShareLink(ctx, "quark", "missing"); !drive.IsShareErrorCode(err, drive.ShareErrNotFound) {
		t.Fatalf("error = %v, want share_not_found", err)
	}
}

func TestUpdateShareLinkHonorsUpdatePolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	provider := newFakeShareProvider()
	provider.policy.Update = false
	provider.addShare(drive.Share{NativeID: "native1", LinkID: "link00000001"})
	c := newShareCore(t, map[string]*fakeShareProvider{"quark": provider})

	title := "new"
	if _, err := c.UpdateShareLink(ctx, ShareLinkUpdateRequest{Mount: "quark", ID: "link00000001", Title: &title}); !errors.Is(err, ErrShareUnsupported) {
		t.Fatalf("error = %v, want ErrShareUnsupported", err)
	}
	if len(provider.updates) != 0 {
		t.Fatal("provider received an update it does not support")
	}
}
