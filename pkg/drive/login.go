package drive

import (
	"context"
	"sort"
	"sync"
)

// LoginUI presents an out-of-band login challenge to the user. The CLI owns
// rendering; a provider only supplies the URL to present. ShowChallenge may be
// called more than once when a challenge expires and is reissued.
type LoginUI interface {
	ShowChallenge(url string) error
}

// LoginResult is the outcome of a successful interactive login. Params carry
// credential-scope driver parameters (for example cookie or refresh_token);
// Account is the provider account identity when the provider can report it.
type LoginResult struct {
	Params  Params
	Account string
}

// LoginProvider acquires credential-scope parameters through an interactive
// flow that the CLI cannot perform on its own, such as scanning a QR code.
// Providers are registered independently of Driver factories because a Driver
// cannot be constructed before the credential exists.
type LoginProvider interface {
	// Login runs the flow until it succeeds, the context is done, or it fails.
	Login(ctx context.Context, ui LoginUI) (LoginResult, error)
	// Account reports the account identity already encoded in params, when it
	// can be derived offline. ok=false means "unknown", not "no account".
	Account(params Params) (account string, ok bool)
}

var (
	loginProvidersMu sync.RWMutex
	loginProviders   = map[string]LoginProvider{}
)

// RegisterLoginProvider makes an interactive login provider available by
// driver type. It is separate from Register so a driver can declare login
// support without changing its factory contract.
func RegisterLoginProvider(name string, provider LoginProvider) {
	loginProvidersMu.Lock()
	defer loginProvidersMu.Unlock()
	loginProviders[name] = provider
}

// LoginProviderFor returns the registered login provider for a driver type.
func LoginProviderFor(name string) (LoginProvider, bool) {
	loginProvidersMu.RLock()
	defer loginProvidersMu.RUnlock()
	provider, ok := loginProviders[name]
	return provider, ok
}

// LoginProviderNames returns the driver types that support interactive login,
// in alphabetical order.
func LoginProviderNames() []string {
	loginProvidersMu.RLock()
	defer loginProvidersMu.RUnlock()
	names := make([]string, 0, len(loginProviders))
	for name := range loginProviders {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
