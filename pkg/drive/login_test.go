package drive

import (
	"context"
	"testing"
	"time"
)

type stubLoginProvider struct {
	account string
	known   bool
	calls   int
}

func (p *stubLoginProvider) Login(ctx context.Context, ui LoginUI) (LoginResult, error) {
	p.calls++
	if ui != nil {
		_ = ui.ShowChallenge("https://example.invalid/challenge")
		_ = ui.ShowChallenge("https://example.invalid/challenge-2")
	}
	return LoginResult{Params: Params{"cookie": "k=v"}, Account: p.account}, nil
}

func (p *stubLoginProvider) Account(Params) (string, bool) {
	return p.account, p.known
}

func TestLoginProviderRegistry(t *testing.T) {
	first := "test-login-a"
	second := "test-login-b"
	RegisterLoginProvider(second, &stubLoginProvider{})
	RegisterLoginProvider(first, &stubLoginProvider{})

	provider, ok := LoginProviderFor(first)
	if !ok {
		t.Fatalf("LoginProviderFor(%q) not found", first)
	}
	result, err := provider.Login(context.Background(), nil)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.Params["cookie"] != "k=v" {
		t.Fatalf("params = %#v, want cookie", result.Params)
	}

	if _, ok := LoginProviderFor("test-login-missing"); ok {
		t.Fatal("unknown provider should not resolve")
	}

	names := LoginProviderNames()
	index := func(name string) int {
		for i, item := range names {
			if item == name {
				return i
			}
		}
		return -1
	}
	if index(first) < 0 || index(second) < 0 {
		t.Fatalf("names = %v, want both registered providers", names)
	}
	if index(first) > index(second) {
		t.Fatalf("names = %v, want alphabetical order", names)
	}
}

func TestLoginProviderAccountUnknown(t *testing.T) {
	provider := &stubLoginProvider{known: false}
	if account, ok := provider.Account(Params{"cookie": "k=v"}); ok || account != "" {
		t.Fatalf("Account = (%q, %v), want unknown", account, ok)
	}
}

type deadlineLoginProvider struct {
	sawDeadline bool
}

func (p *deadlineLoginProvider) Login(ctx context.Context, ui LoginUI) (LoginResult, error) {
	_, p.sawDeadline = ctx.Deadline()
	return LoginResult{}, nil
}

func (p *deadlineLoginProvider) Account(Params) (string, bool) { return "", false }

func TestLoginProviderReceivesCallerContext(t *testing.T) {
	provider := &deadlineLoginProvider{}
	RegisterLoginProvider("test-login-context", provider)
	resolved, ok := LoginProviderFor("test-login-context")
	if !ok {
		t.Fatal("provider not registered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := resolved.Login(ctx, nil); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !provider.sawDeadline {
		t.Fatal("provider did not receive the caller's deadline")
	}
}
